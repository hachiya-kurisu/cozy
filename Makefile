all: cozy

again: clean all

cozy: cozy.go cmd/cozy/main.go
	go build -o cozy cmd/cozy/main.go

clean:
	rm -f cozy

test:
	go test -cover

push:
	got send
	git push github

fmt:
	gofmt -s -w *.go cmd/*/main.go

README.md: README.gmi
	sisyphus -f markdown <README.gmi >README.md

doc: README.md

release: push
	git push github --tags
