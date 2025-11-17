# llm-duet

`llm-duet` は、Claude Code と Codex を呼び出して `plan.md` を育てる Go 製 CLI です。初期化と計画更新の 2 コマンドを提供します。

## インストールとビルド

```bash
go build ./cmd/llm-duet
```

## 使い方

### 初期化

プロジェクトルートで `.llm-duet/` を作成し、プロンプトや workspace を準備します。既存ファイルは上書きしません。

```bash
./llm-duet init
```

### plan.md の作成・更新

LLM CLI が利用可能であることを前提に、設計書を生成・改善します。初回は `--idea` か `--idea-file` が必須です。

```bash
# 初回
./llm-duet plan --idea "作りたいもの"

# 2 回目以降（必要に応じてヒントを渡す）
./llm-duet plan --hint "今回重視してほしい点"
```

`--hint` と `--hint-file` を併用すると、ファイル内容の末尾に空行を挟んで追記したテキストがヒントとして渡されます。

### エラー時の挙動

- `claude` / `codex` が見つからない場合や lock ファイルが残っている場合はエラー終了します。
- LLM 実行や JSON バリデーションに失敗した場合、`.llm-duet/workspace/` に生成済みファイルを残したまま終了します。
