# Definition of Done

After completing your implementation, review your own changes against each criterion below. These are quality checks you perform by inspecting your work — not commands to run (linting and tests already ran via `validationCommand`). Report any unmet criterion as a blocker.

## Code Quality

- Exported types, functions, and interfaces have doc comments
- Error handling follows project conventions (`errors.Wrap(ctx, err, "...")` from `github.com/bborbe/errors`; never `fmt.Errorf`)
- No debug output (print statements, `fmt.Printf`) — use structured logging
- Follows Interface → Constructor → Struct → Method pattern
- Uses `github.com/bborbe/time` instead of the standard `time` package

## Testing

- New code has good test coverage (target >= 80%)
- Changes to existing code have tests covering at least the changed behavior
- Tests use Ginkgo v2 + Gomega; mocks generated via Counterfeiter `//counterfeiter:generate`

## Dependencies

- If `make precommit` fails due to a dependency vulnerability with a known fix version, update the dependency (`go get <pkg>@<fixed-version> && go mod tidy`) as part of your change
- No `exclude` or `replace` directives in go.mod (break remote install)
- `vendor/` is a build-time artifact: `check-go-mod` regenerates it via `go mod vendor` as part of `make build`, and `make precommit`'s `ensure` target removes it. Never commit it.

## Documentation

- README.md is updated if the change affects usage, configuration, or setup
- When changing CLI args, config fields, env vars, or flags (add, rename, remove, change default), grep the entire repo and update all references in docs/, README.md, examples, and comments
- CHANGELOG.md has an entry under `## Unreleased`
