# Ephemeral Pod

高速かつ揮発性を重視したファイル共有サービス「Ephemeral Pod」のリポジトリです。フロントエンドは Astro、バックエンドは Go で実装されています。ブラウザ側で暗号化・復号を行い、サーバは暗号化済みデータを tmpfs 上に短時間保持するだけの構成です。

## 構成概要

- フロントエンド: Astro + Vite 開発サーバ、URL ハッシュに暗号鍵・ファイル名を埋め込み
- バックエンド: Go (chi, gorilla/websocket) / `cmd/server`
- ストレージ: `EPHEMERALPOD_STORAGE_PATH` (デフォルト `/dev/shm/ephemeralpod`)
  - 起動時にディレクトリ内を完全削除
  - TTL・WebSocket切断時に自動削除
  - 総容量を超える際は古いファイルから自動削除

## 事前準備

- Node.js 18+ / npm
- Go 1.22+
- Linux 系 OS（tmpfs を利用）
- TLS 終端用のリバースプロキシ（本番運用時）

## 環境変数

| 変数名 | 説明 | 既定値 |
| --- | --- | --- |
| `EPHEMERALPOD_ADDR` | バックエンド待受アドレス | `:8080` |
| `EPHEMERALPOD_STORAGE_PATH` | 暗号化ファイル保存先 (tmpfs 推奨) | `/dev/shm/ephemeralpod` |
| `EPHEMERALPOD_STORAGE_CAPACITY_MB` | 保存領域の論理上限。超過時は最古ファイルから自動削除 | `12288` (12GB) |
| `EPHEMERALPOD_MAX_UPLOAD_SIZE_MB` | 1 ファイルの最大アップロードサイズ | `200` |
| `EPHEMERALPOD_UPLOAD_TTL` | ファイル有効期限 (`30m`, `8h` など) | `30m` |
| `EPHEMERALPOD_CLEANUP_INTERVAL` | TTL 監視クリーンアップ間隔 | `1m` |
| `EPHEMERALPOD_WS_IDLE_PING` | WebSocket 書き込み期限 | `30s` |
| `EPHEMERALPOD_ALLOWED_ORIGINS` | CORS 許可オリジン (カンマ区切り) | `*` |
| `PUBLIC_API_BASE` | フロントエンドから見た API のベース URL | なし (相対パス) |
| `PUBLIC_WS_BASE` | フロントエンドから見た WebSocket ベース URL | なし (相対指定) |

## バックエンドのセットアップと起動

```bash
cd backend
# 環境変数を設定 (例)
export EPHEMERALPOD_ADDR=":8080"
export EPHEMERALPOD_STORAGE_PATH="/dev/shm/ephemeralpod"
export EPHEMERALPOD_UPLOAD_TTL="8h"
export EPHEMERALPOD_ALLOWED_ORIGINS="https://your.domain"

go build ./...
go run ./cmd/server
```

起動時に `EPHEMERALPOD_STORAGE_PATH` 内のファイルは全削除されます。稼働中に容量上限を超えると、アップロード日時の古いファイルから順に削除して空きを確保します。

## フロントエンドのセットアップと起動

```bash
cd frontend
npm install

# 開発サーバ起動 (バックエンドが :8080 で動作している想定)
PUBLIC_API_BASE="http://localhost:8080" \
PUBLIC_WS_BASE="ws://localhost:8080" \
npm run dev
```

ビルド後の静的ファイルは `npm run build` で生成されます。

## 動作確認の流れ

1. フロントエンドでファイルを選択するとブラウザ側で AES-GCM 暗号化が行われる
2. `POST /api/upload` に暗号化済みファイルを送信
3. バックエンドはファイル ID と TTL を返却し、共有 URL (`#/key=...&name=...`) をフロントエンドが生成
4. ダウンロードリンクを開いたブラウザは `GET /download/{id}` でメタデータを取得し、WebSocket (`/ws/connect/{id}`) で暗号化データを受信
5. URL ハッシュの鍵を用いて復号し、自動的にダウンロード開始

## セキュリティ上の注意

- サーバは暗号鍵やファイル名を保持しません (URL ハッシュにのみ存在)
- API レスポンスにセキュリティヘッダー (CSP 等) を付与しています
- TLS で終端し、`EPHEMERALPOD_ALLOWED_ORIGINS` を本番ドメインに限定してください
- 監査ログなどの運用基盤は別途整備してください

## systemd による常駐運用

`ops/systemd/` 以下にバックエンドとフロントエンドのサービスユニット雛形と環境変数ファイルを用意しています。

1. バックエンドをビルドして配置

  ```bash
  sudo useradd --system --home /opt/ephemeralpod --shell /usr/sbin/nologin ephemeralpod
  sudo mkdir -p /opt/ephemeralpod/{backend,frontend}
  go build -o backend/bin/ephemeralpod ./cmd/server
  sudo cp backend/bin/ephemeralpod /opt/ephemeralpod/backend/ephemeralpod
  sudo chown -R ephemeralpod:ephemeralpod /opt/ephemeralpod
  ```

1. フロントエンドをビルドして配置

  ```bash
  cd frontend
  npm install
  npm run build
  sudo rsync -a dist/ /opt/ephemeralpod/frontend/
  sudo chown -R ephemeralpod:ephemeralpod /opt/ephemeralpod/frontend
  ```

  `npm run preview` を使用するため、`package.json` のスクリプト定義を確認してください。必要に応じて Node.js の実行パスを調整します。

1. サービスユニットと環境ファイルを設置

  ```bash
  sudo mkdir -p /etc/ephemeralpod
  sudo cp ops/systemd/ephemeralpod-backend.env /etc/ephemeralpod/backend.env
  sudo cp ops/systemd/ephemeralpod-frontend.env /etc/ephemeralpod/frontend.env
  sudo cp ops/systemd/ephemeralpod-backend.service /etc/systemd/system/
  sudo cp ops/systemd/ephemeralpod-frontend.service /etc/systemd/system/
  ```

  環境ファイル内の値 (`PUBLIC_API_BASE` など) を本番環境に合わせて編集してください。

1. サービスの読み込みと起動

  ```bash
  sudo systemctl daemon-reload
  sudo systemctl enable --now ephemeralpod-backend.service
  sudo systemctl enable --now ephemeralpod-frontend.service
  ```

ユニットファイルには `ProtectSystem=strict` などのハードニング設定を含めています。必要に応じて `ReadWritePaths` を調整してください。

