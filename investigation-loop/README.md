# investigation-loop

ADK Go 2.x の dynamic workflow で書いた「分かるまで調べる」インシデント調査ループと、そのガードレール。

Claude Agent SDK の agent loop は `max_turns` / `max_budget_usd` / ツール許可を標準で持つ。
ADK の `llmagent` も同じ「モデル → ツール → モデル」のループを回すが、上限は持たない
（ツール呼び出しのない応答が返るまで回り続ける）。このサンプルはその上限を自分で組み立てる。

## 構造

```
START ─> investigate (dynamic, Go) ──RunNode × N──> investigator (llmagent + tools)
```

- `investigate` は `workflow.NewDynamicNode` で書いた Go のオーケストレータ。ラウンドを回し、
  ラウンド間で引き継ぐもの（確定した findings）と停止条件を決める
- `investigator` は read-only ツールを持つ `llmagent`。1 ラウンドの中で agent loop を回し、
  JSON で findings を返す。single-turn ノードなので前のラウンドの会話は見えない

## ガードレール

| 層 | ガードレール | 実装 | 既定値 |
|---|---|---|---|
| ループ（Go） | 最大ラウンド数 | `investigate` の for 条件 | 6 |
| ループ（Go） | 進捗なし停止 | 新しい finding が N ラウンド連続で出なければ止める | 2 |
| ループ（Go） | LLM の done 提案 | finding が 1 件以上あるときだけ受け入れる | - |
| ループ（Go） | 不正な出力 | JSON として読めない応答は finding を増やせない | - |
| 両方 | トークン予算 | `AfterModelCallback` で集計、ラウンド開始前と `BeforeModelCallback` で判定 | 30,000 |
| agent loop | ラウンドあたりのモデル呼び出し上限 | `BeforeModelCallback` がモデルの代わりに空の結果を返す | 8 |
| agent loop | ツール allowlist | `BeforeToolCallback` が read-only 以外を拒否し、拒否をツール結果として返す | 3 ツール |
| agent loop | 引数の制限 | `BeforeToolCallback` が `window_minutes` を上限に丸める | 60 |
| スケジューラ | ラウンドのタイムアウト | `NodeConfig.Timeout` | 2 分 |

`restart_pod` はわざとツール一覧に入れてある。MCP toolset ではサーバが公開する書き込み系ツールも
そのまま入ってくるため、「危険なツールを渡さない」ではなく allowlist で止める形にしている。

## 実行

API キーなしで動く。`GOOGLE_API_KEY` が未設定ならスクリプト化したスタブモデルを使う。

```bash
go run . console
```

Gemini を使う場合:

```bash
export GOOGLE_API_KEY=...
go run . console
```

## シナリオ

スタブモデルの振る舞いを `INVESTIGATION_SCENARIO` で切り替えると、各ガードレールが発動するところを見られる。

| 値 | スタブの振る舞い | 止めるガードレール |
|---|---|---|
| `converge`（既定） | 3 ラウンドで 3 件見つけ、その後は既知の finding しか返さない。途中で `restart_pod` を呼ぶ | 進捗なし停止（5 ラウンド）。`restart_pod` は allowlist で拒否 |
| `runaway-tools` | ツールを呼び続けて答えない | ラウンドごとにモデル呼び出し上限 → 進捗なし停止（2 ラウンド） |
| `endless-findings` | 毎ラウンド「新しい」finding を出す | トークン予算（5 ラウンド、30,000 tokens） |

```bash
INVESTIGATION_SCENARIO=runaway-tools go run . console
```

## テスト

```bash
go test ./...
```

3 シナリオを ADK の runner で端から端まで実行し、停止理由、`restart_pod` が一度も実行されないこと、
`window_minutes` が丸められることを確認する。

## 制限

- ガードレールの状態はプロセスのメモリにある。別プロセスで resume するとトークン集計が 0 から始まる
- context compaction は使っていない。`runner.Config.Compaction` は Summarizer を指定しないと
  root agent が LLM agent であることを要求し、このサンプルの root は workflow agent のため
