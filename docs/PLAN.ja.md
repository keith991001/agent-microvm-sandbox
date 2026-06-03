# microVM サンドボックス — 段階的開発計画と結果（講師共有用）

**コンセプト**：macOS の Virtualization.framework をベースに、「1コマンド = 1 microVM」の
実行モデルを Go で試作する（Firecracker が目指している世界観の小さい版）。コマンドごとに
使い捨ての軽量 Linux microVM を起動して実行し、終了後に破棄する。

**技術スタック**：Go + Virtualization.framework（`Code-Hex/vz`）/ ゲストは ARM64 Linux /
Apple Silicon Mac ローカルで完結。

各ステージは終了時に必ず「動かせる・デモできる状態」になるよう分割した（増分開発）。
結果として **S0〜S7 をすべて実装完了**。

## ステージ一覧（すべて完了）

| ステージ | 目標 | 動作可能な状態 | 状態 |
|------|------|------------------|------|
| **S0** | ビルド→署名→entitlement 疎通 | 署名済みバイナリが実行でき、virtualization entitlement を確認 | ✅ |
| **S1** | 最初の Linux VM 起動 | カーネル+initrd を起動、シリアルに shell | ✅ |
| **S2** | コードからの起動 | プログラムで VM を構成・起動 | ✅ |
| **S3** | 1コマンド=1microVM (MVP) | `microvm "<cmd>"` で起動→実行→出力+終了コード→破棄 | ✅ |
| **S4** | クリーンな出力経路 | vsock + ゲストエージェントで stdout/stderr/終了コードを JSON 取得 | ✅ |
| **S5** | 隔離の強化 | ネット遮断 / 根ro / 使い捨て tmpfs / コマンドタイムアウト | ✅ |
| **S6** | ウォームプール | 事前起動 VM プールでコールドスタート解消 | ✅ |
| **S7** | サービス化 | `POST /run {"cmd":...}` の HTTP サービス | ✅ |

## 実測：起動最適化の推移（端到端 1 コマンド）

| 方式 | レイテンシ |
|------|--------:|
| フル Ubuntu 起動（systemd + cloud-init） | ~125 s |
| OS 起動を飛ばす（`init=/bin/sh`） | ~3.7 s |
| クリーンな通信路（vsock） | ~2.1 s |
| **ウォームプール命中** | **~35 ms** |

## 隔離（実測で確認）

| 性質 | 実現方法 | 確認 |
|------|---------|------|
| ホストへ脱出できない | microVM のハードウェア仮想化境界 | 設計上 |
| ネットワーク不可 | NIC を付けない | 外部接続 → "Network is unreachable" |
| ベースイメージを汚さない | 根を読み取り専用マウント | `/etc` 書込 → "Read-only file system" |
| 書き込みは使い捨て | `/tmp` に tmpfs | 次回の新規 VM では消えている |
| 無限ハングしない | コマンド 30s タイムアウト（プロセスグループごと kill） | `sleep 60` → ~30s で exit 124 |
| リソース上限 | 2 vCPU / 1 GiB / VM | — |

## アーキテクチャ

```
host (main.go)                                  guest microVM
  CLI / HTTP サービス / ウォームプール   vsock   kernel + Ubuntu rootfs(ro) + tmpfs
      ── connect / command ───────────────────►  guest-agent が bash で実行
      ◄── JSON{stdout,stderr,exit} ────────────  実行後 poweroff
        virtio-fs: agent バイナリを guest へ配送
        シリアル: 初回の3行ブートストラップのみ
```

## 設計上のポイント

- **`init=/bin/sh`** で systemd/cloud-init を飛ばし、起動を 125s → 数秒に短縮（Ubuntu のユーザランドは利用可能）。
- **vsock + JSON** にしたことで、シリアルからの出力スクレイプではなく構造化された結果（stdout/stderr/終了コード）を取得。
- **virtio-fs** で agent を guest に配送（macOS から ext4 へ書き込めない問題を回避）。
- **ウォームプール** で事前起動し、命中時は数十ミリ秒で応答。
- タイムアウトはプロセスグループごと kill（孫プロセスがパイプを掴んだままにならないように）。

## リポジトリ

https://github.com/keith991001/agent-microvm-sandbox
