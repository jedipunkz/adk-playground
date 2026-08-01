# sre-agent

ADK Go 2.0 のグラフワークフローエンジンで書いたインシデントトリアージエージェント。

設計の主張はひとつだけで、**LLM は診断を書き、分岐は決定的な Go のコードが決める**。
エージェントが取りうるアクションの全集合は `graph.go` のルーティングエッジとして静的に
宣言されており、本番に変更を加える唯一のアクション（ロールバック）は Human-in-the-Loop
の承認ゲートの後ろに置いてある。

記事: [ADK Go 2.0 のグラフワークフローを読んで SRE Agent への応用を考える](https://jedipunkz.rocks/post/adk-go-2/)

## グラフ

```
START ─┬─> fetch_metrics ─┐
       ├─> fetch_logs ────┼─> gather ─> build_evidence ─> diagnose ─> decide_action ─┬─> propose_rollback (HITL)
       └─> fetch_deploys ─┘  (Join)        (Go)             (LLM)          (Go)      ├─> scale_out_service
                                                                                    └─> page_oncall
```

`diagnose` は分岐の手前にいるが、分岐の入力ではない。`decide_action` は LLM の出力ではなく
`build_evidence` が session state に書いた生の証拠 (`Evidence`) だけを読んでルートを決める。

## 実行

API キーなしで動く。`GOOGLE_API_KEY` が未設定なら決定的なスタブ診断モデルにフォールバックする。

```bash
go run . console
```

Gemini を使う場合:

```bash
export GOOGLE_API_KEY=...
go run . console
```

Dev UI:

```bash
go run . web
```

`checkout-api のエラー率が上がっている` のようにサービス名を含むメッセージを送ると
トリアージが走る。サービス名は `evidence.go` の `knownServices` にある値だけを受け付け、
それ以外は `checkout-api` にフォールバックする。

## シナリオ

コレクタは外部バックエンドを叩かず、固定されたインシデントを返す。`SRE_AGENT_SCENARIO`
で切り替えると 3 つの分岐すべてを実際に通せる。

| 値 | 内容 | 到達するノード |
|---|---|---|
| `deploy-regression` (既定) | デプロイ 7 分後にエラー率 18% | `propose_rollback`（承認待ちで中断する） |
| `saturation` | CPU 93%、エラー率はバジェット内 | `scale_out_service` |
| `unknown-cause` | エラー率は高いが直近デプロイもサチュレーションもなし | `page_oncall` |

```bash
SRE_AGENT_SCENARIO=saturation go run . console
```

`deploy-regression` では承認プロンプトが出るので `yes` か `no` を入力する。`yes` 以外は
すべて拒否として扱われ、本番には何も起きない。

## ファイル

| ファイル | 役割 |
|---|---|
| `main.go` | launcher の配線とモデル選択（Gemini / スタブ） |
| `graph.go` | ノードとエッジの宣言。取りうるアクションの全集合はここにある |
| `evidence.go` | 証拠の型、3 つのコレクタ、fan-in のフォーマッタ |
| `decide.go` | 決定ポリシー。`chooseRoute` が純関数で、閾値もここ |
| `actions.go` | 3 つの Runbook。ロールバックだけ HITL |
| `scenario.go` | 固定インシデントのデータ。実運用では消えるファイル |
| `stubmodel.go` | API キーなしでグラフ全体を通すための決定的なスタブ LLM |

## テスト

```bash
go test ./...
```

`decide_test.go` の `TestDecideActionIgnoresDiagnosis` がこの設計の要で、互いに矛盾する
2 つの LLM 診断を与えても同じルートに落ちることを検証している。LLM の出力が分岐を動かせない
ことを、テストとして書ける形にしてある。

## 実運用に持っていくときに変えるところ

- `scenario.go` を削除し、`fetchMetrics` / `fetchLogs` / `fetchDeploys` を実際の
  observability API 呼び出しに置き換える（Datadog なら
  [datadog-api-client-go](https://github.com/DataDog/datadog-api-client-go) の
  `MetricsApi.QueryTimeseriesData` と Logs Aggregate API）。クエリと時間窓を Go 側で
  固定しているのは意図的で、ここを LLM に委ねると `decide_action` の再現性が失われる。
- `actions.go` の 3 つの Runbook を実際のデプロイ API / オートスケール API /
  ページャに接続する。
- `decide.go` の閾値をサービスごとの設定に外出しするかは要検討。外出しすると
  「グラフを読めば挙動が分かる」という利点が薄れる。
