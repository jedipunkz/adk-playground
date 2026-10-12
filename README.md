# adk-playground

[ADK (Agent Development Kit) for Go](https://github.com/google/adk-go) を触りながら書いた
実験用エージェントを置いておくリポジトリ。各ディレクトリが独立した Go モジュール。

| ディレクトリ | 内容 |
|---|---|
| [`sre-agent`](./sre-agent) | ADK Go 2.0 のグラフワークフローで書いたインシデントトリアージエージェント。LLM は診断を書き、Runbook の選択は決定的な Go のコードが行う |
| [`investigation-loop`](./investigation-loop) | ADK Go 2.x の dynamic workflow で「分かるまで調べる」調査ループを回し、ラウンド上限・進捗なし停止・トークン予算・モデル呼び出し上限・ツール allowlist をガードレールとして組み込んだサンプル |

## 必要なもの

- Go 1.26.5 以上（`sre-agent` が使う `google.golang.org/adk/v2` v2.1.0 の要求）。`investigation-loop` は v2.5.0 を使うため Go 1.26.6 以上
- Gemini を使う場合は `GOOGLE_API_KEY`。各サンプルは API キーなしでも動くようにしてある

## 関連記事

- [ADK Go 2.0 のグラフワークフローを読んで SRE Agent への応用を考える](https://jedipunkz.rocks/post/adk-go-2/)
