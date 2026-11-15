.PHONY: dev seed test build

dev:
	./scripts/dev.sh
seed:
	python3 scripts/seed.py
test:
	./scripts/test.sh
build:
	cd backend && go build -o alliance .
	npm --prefix frontend run build
