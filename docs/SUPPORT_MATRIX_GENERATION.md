# Support Matrix Generation

```sh
go generate ./...
```

Generation flow:

```text
support_matrix_generate.go
  -> scripts/generate-support-matrix.sh
  -> internal/generate/supportmatrix
  -> internal/supportmatrix
  -> docs/API_SUPPORT.md
```

Validation gates:

- `support_matrix_test.go`: every registered Agent must have exactly one capability profile.
- `internal/supportmatrix/matrix_test.go`: every profile must define every canonical feature, and the checked-in document must be current.
- `tests/support_matrix_e2e_test.go`: executes the real generation hook and compares the complete output.

Canonical feature identifiers such as `agent.initialize`, `session.list`, and `turn.stream` are shared across language SDKs. Language-specific APIs map onto these identifiers.
