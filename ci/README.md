# Staged workflows

Cloud sessions cannot push `.github/workflows/**`. Put the intended workflow here under the same file name; a person copies it into `.github/workflows`. `factory doctor` warns and `go run ./tools/cidrift` lists each file that differs or has no live copy.
