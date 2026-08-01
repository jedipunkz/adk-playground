# adk-playground

[ADK (Agent Development Kit) for Go](https://github.com/google/adk-go) を触りながら書いた
実験用エージェントを置いておくリポジトリ。各ディレクトリが独立した Go モジュール。

| ディレクトリ | 内容 |
|---|---|
| [`sre-agent`](./sre-agent) | ADK Go 2.0 のグラフワークフローで書いたインシデントトリアージエージェント。LLM は診断を書き、Runbook の選択は決定的な Go のコードが行う |

## 必要なもの

- Go 1.26.5 以上（`google.golang.org/adk/v2` v2.1.0 の要求）
- Gemini を使う場合は `GOOGLE_API_KEY`。各サンプルは API キーなしでも動くようにしてある

## 関連記事

- [ADK Go 2.0 のグラフワークフローを読んで SRE Agent への応用を考える](https://jedipunkz.rocks/post/adk-go-2/)
