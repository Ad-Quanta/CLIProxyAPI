# Contributing

This project uses `main` as the stable branch.

## Branches

Create a short-lived branch from an up-to-date `main`:

```bash
git fetch origin
git switch main
git pull --ff-only
git switch -c feature/<name>
```

Use `feature/<name>` for features, `fix/<name>` for fixes, `docs/<name>` for documentation, and `chore/<name>` for maintenance.

Do not develop directly on `main`. Keep one topic per branch and pull request.

## Commits

Use a short English subject with a conventional prefix:

```text
feat(scope): add ...
fix(scope): correct ...
test(scope): add ...
docs(scope): update ...
chore(scope): maintain ...
```

Do not commit credentials, OAuth files, databases, logs, local deployment files, or production configuration.

## Pull requests

A pull request should state:

- what changed;
- why it changed;
- how it was tested;
- whether configuration, Home, compatibility, or deployment is affected.

Keep unrelated formatting and cleanup out of the pull request. Changes to retry, fallback, streaming, payload construction, or Home protocols require regression tests.

## Before requesting review

```bash
git status
git diff
gofmt -w <changed-go-files>
go test ./...
go build -o test-output ./cmd/server
rm -f test-output
```

Push the branch and open a pull request against the team's `main` branch:

```bash
git push -u origin <branch-name>
```

`origin` is the team repository. `upstream` is the official repository.

