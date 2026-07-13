# 代码生成

```sh
go generate ./...
```

能力矩阵生成链路：

```text
support_matrix_generate.go
  -> scripts/generate-support-matrix.sh
  -> internal/generate/supportmatrix
  -> internal/supportmatrix
  -> docs/API_SUPPORT.md
```

校验：

- `support_matrix_test.go`：Agent 注册与 capability profile 必须一一对应。
- `internal/supportmatrix/matrix_test.go`：profile 必须完整，生成文档不能漂移。
- `tests/support_matrix_e2e_test.go`：执行真实生成脚本，并比较完整输出。
