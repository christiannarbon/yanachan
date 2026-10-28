# Yana-chan 4K
#
# Local:
#   make deps      install the Go and npm dependencies
#   make run       build everything and serve the dashboard on one port
#   make dev       hot-reloading backend and frontend on two ports
#   make down      stop everything running locally
#
# Kubernetes:
#   make k8s-up    cluster, image, deploy, wait, tunnel -- one command
#   make k8s-down  tear it all back down again
#
# Run `make help` for the full list.

SHELL := /bin/bash

API_PORT      ?= 19080
WEB_PORT      ?= 19090
API_HOST      ?= 127.0.0.1

IMAGE_NAME    ?= yana-chan-4k
IMAGE_TAG     ?= dev
IMAGE         := $(IMAGE_NAME):$(IMAGE_TAG)

ROOT      := $(shell pwd)
BACKEND   := $(ROOT)/backend
FRONTEND  := $(ROOT)/frontend
EMBED_DIR := $(BACKEND)/internal/webui/dist
BIN       := $(ROOT)/bin/yana-chan-4k
RUN_DIR   := $(ROOT)/.run

DEV_ORIGIN := http://localhost:$(WEB_PORT)

# Kubernetes
NS         := yana-chan-4k
K8S_DIR    := k8s
OVERLAY    ?= dev
K8S_SECRET := yana-chan-4k
PF_PORT    ?= $(API_PORT)
PF_PID     := .k8s-portforward.pid
PF_LOG     := .k8s-portforward.log

# A minikube profile is not always called "minikube" -- the kube context carries
# the profile name, so ask minikube whether it owns the context rather than
# matching the name. Expects $$ctx to be set by the recipe using it.
IS_MINIKUBE = minikube profile list -o json 2>/dev/null | grep -q '"Name":"'"$$ctx"'"'

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "Yana-chan 4K"
	@echo ""
	@echo "  API port $(API_PORT)   dev web port $(WEB_PORT)   image $(IMAGE)   overlay $(OVERLAY)"
	@echo ""
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[32m%-22s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- prepare ---

.PHONY: deps
deps: deps-backend deps-frontend ## Install all dependencies

.PHONY: deps-backend
deps-backend: ## Download the Go module cache
	@cd $(BACKEND) && go mod download && go mod tidy

.PHONY: deps-frontend
deps-frontend: ## Install the npm packages
	@cd $(FRONTEND) && npm install

.PHONY: doctor
doctor: ## Check that the tools this project needs are present
	@echo "checking toolchain"
	@command -v go >/dev/null   && echo "  go        $$(go version | awk '{print $$3}')"        || echo "  go        MISSING"
	@command -v node >/dev/null && echo "  node      $$(node -v)"                               || echo "  node      MISSING"
	@command -v npm >/dev/null  && echo "  npm       $$(npm -v)"                                || echo "  npm       MISSING"
	@command -v gh >/dev/null   && echo "  gh        $$(gh --version | head -1 | awk '{print $$3}')" || echo "  gh        MISSING (optional: OAuth sign-in still works)"
	@command -v gh >/dev/null   && (gh auth status >/dev/null 2>&1 && echo "  gh auth   logged in" || echo "  gh auth   not logged in")
	@command -v docker >/dev/null  && echo "  docker    $$(docker --version | awk '{print $$3}' | tr -d ,)" || echo "  docker    MISSING (optional)"
	@command -v kubectl >/dev/null && echo "  kubectl   present"                                 || echo "  kubectl   MISSING (optional)"
	@command -v minikube >/dev/null && echo "  minikube  $$(minikube version --short 2>/dev/null)" || echo "  minikube  MISSING (optional)"

# ------------------------------------------------------------------ build ---

.PHONY: build
build: build-frontend build-backend ## Build the frontend bundle and the server binary

.PHONY: build-frontend
build-frontend: ## Compile the Vue app and stage it for embedding
	@cd $(FRONTEND) && npm run build
	@rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@cp -R $(FRONTEND)/dist/. $(EMBED_DIR)/
	@echo "frontend staged in $(EMBED_DIR)"

.PHONY: build-backend
build-backend: ## Compile the Go server with the frontend embedded
	@mkdir -p $(ROOT)/bin
	@cd $(BACKEND) && go build -trimpath -o $(BIN) ./cmd/server
	@echo "built $(BIN)"

.PHONY: test
test: ## Run the Go tests and the frontend type check
	@cd $(BACKEND) && go test ./...
	@cd $(FRONTEND) && npm run typecheck

.PHONY: fmt
fmt: ## Format the Go sources
	@cd $(BACKEND) && gofmt -w . && go vet ./...

.PHONY: docs-check
docs-check: ## Check every internal link in the documentation
	@node $(ROOT)/scripts/check-doc-links.mjs

.PHONY: check
check: fmt test docs-check ## Format, vet, test and check the docs

# -------------------------------------------------------------------- run ---

.PHONY: run
run: build ## Build and serve the whole dashboard on the API port
	@echo "serving on http://$(API_HOST):$(API_PORT)"
	@GHDASH_ADDR=$(API_HOST):$(API_PORT) $(BIN)

.PHONY: dev
dev: dev-api dev-web ## Start the backend and the vite dev server in the background
	@echo ""
	@echo "  backend  http://$(API_HOST):$(API_PORT)   log: $(RUN_DIR)/api.log"
	@echo "  frontend http://localhost:$(WEB_PORT)     log: $(RUN_DIR)/web.log"
	@echo ""
	@echo "  open http://localhost:$(WEB_PORT)"
	@echo "  stop with: make down"

.PHONY: dev-api
dev-api: ## Start only the backend, in the background
	@mkdir -p $(RUN_DIR)
	@if [ -f $(RUN_DIR)/api.pid ] && kill -0 $$(cat $(RUN_DIR)/api.pid) 2>/dev/null; then \
		echo "backend already running (pid $$(cat $(RUN_DIR)/api.pid))"; \
	else \
		cd $(BACKEND) && GHDASH_ADDR=$(API_HOST):$(API_PORT) GHDASH_DEV_ORIGIN=$(DEV_ORIGIN) \
			nohup go run ./cmd/server > $(RUN_DIR)/api.log 2>&1 & echo $$! > $(RUN_DIR)/api.pid; \
		echo "backend starting (pid $$(cat $(RUN_DIR)/api.pid))"; \
	fi

.PHONY: dev-web
dev-web: ## Start only the vite dev server, in the background
	@mkdir -p $(RUN_DIR)
	@if [ -f $(RUN_DIR)/web.pid ] && kill -0 $$(cat $(RUN_DIR)/web.pid) 2>/dev/null; then \
		echo "frontend already running (pid $$(cat $(RUN_DIR)/web.pid))"; \
	else \
		cd $(FRONTEND) && GHDASH_API_PORT=$(API_PORT) GHDASH_WEB_PORT=$(WEB_PORT) \
			nohup npm run dev > $(RUN_DIR)/web.log 2>&1 & echo $$! > $(RUN_DIR)/web.pid; \
		echo "frontend starting (pid $$(cat $(RUN_DIR)/web.pid))"; \
	fi

.PHONY: logs
logs: ## Tail the background dev logs
	@tail -f $(RUN_DIR)/api.log $(RUN_DIR)/web.log

# ----------------------------------------------------------------- docker ---

.PHONY: images
images: ## Build the container image
	docker build -f $(ROOT)/deploy/docker/Dockerfile -t $(IMAGE) $(ROOT)

.PHONY: docker-build
docker-build: images ## Alias for images

.PHONY: docker-up
docker-up: ## Run the image with docker compose on the API port
	@set -e; \
	token="$${GH_TOKEN:-}"; \
	if [ -z "$$token" ] && [ -t 0 ] && command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then \
		echo "gh CLI is installed and logged in on this machine."; \
		echo "The container cannot reach it, but the token behind that session can be"; \
		echo "forwarded as GH_TOKEN. You still have to approve it in the app."; \
		read -r -p "Forward your gh CLI token into the container? [y/N] " reply; \
		case "$$reply" in [yY]*) token="$$(gh auth token)";; *) echo "not forwarding; use OAuth sign-in in the app";; esac; \
	fi; \
	cd $(ROOT)/deploy/docker && GHDASH_API_PORT=$(API_PORT) GHDASH_IMAGE=$(IMAGE) GH_TOKEN="$$token" \
		docker compose up -d --build
	@echo "dashboard on http://$(API_HOST):$(API_PORT)"

.PHONY: docker-down
docker-down: ## Stop the docker compose stack
	@cd $(ROOT)/deploy/docker && GHDASH_IMAGE=$(IMAGE) docker compose down

.PHONY: docker-logs
docker-logs: ## Follow the container logs
	@cd $(ROOT)/deploy/docker && GHDASH_IMAGE=$(IMAGE) docker compose logs -f

# ------------------------------------------------------------- kubernetes ---

.PHONY: k8s-up
k8s-up: ## Bring the whole Kubernetes stack up and open a tunnel to it
	@echo "==> cluster"
	@$(MAKE) --no-print-directory k8s-cluster
	@echo "==> image"
	@$(MAKE) --no-print-directory k8s-load
	@echo "==> secret"
	@$(MAKE) --no-print-directory k8s-secret
	@echo "==> deploy"
	kubectl apply -k $(K8S_DIR)/overlays/$(OVERLAY)
	@echo "==> waiting for the pod"
	kubectl -n $(NS) rollout status deploy/yana-chan-4k --timeout=300s
	@$(MAKE) --no-print-directory k8s-tunnel
	@echo
	@echo "  Yana-chan 4K is up:  http://localhost:$(PF_PORT)"
	@echo "  logs:                make k8s-logs"
	@echo "  status:              make k8s-status"
	@echo "  shut down:           make k8s-down"

.PHONY: k8s-cluster
k8s-cluster: ## Make sure a local cluster is running
	@ctx=$$(kubectl config current-context 2>/dev/null || echo none); \
	if [ "$$ctx" = none ]; then \
		echo "no kube context; start minikube or point kubectl at a cluster" >&2; exit 1; \
	elif $(IS_MINIKUBE); then \
		minikube -p "$$ctx" status >/dev/null 2>&1 || minikube -p "$$ctx" start; \
	else \
		case "$$ctx" in \
			kind-*) kind get clusters | grep -q "$${ctx#kind-}" || { echo "kind cluster $${ctx#kind-} is gone" >&2; exit 1; } ;; \
			*)      echo "  context $$ctx is not minikube or kind: $(IMAGE) must be pullable from it" ;; \
		esac; \
	fi

.PHONY: k8s-load
k8s-load: images ## Build the image and push it into the local cluster
	@ctx=$$(kubectl config current-context 2>/dev/null); \
	if $(IS_MINIKUBE); then \
		minikube -p "$$ctx" image load $(IMAGE); \
	else \
		case "$$ctx" in \
			kind-*) kind load docker-image $(IMAGE) --name "$${ctx#kind-}" ;; \
			*)      echo "  context $$ctx is not minikube or kind; push $(IMAGE) to a registry yourself" ;; \
		esac; \
	fi

.PHONY: k8s-secret
k8s-secret: ## Create or replace the GitHub secret in the cluster
	@set -e; \
	token="$${GH_TOKEN:-}"; \
	if [ -z "$$token" ] && [ -t 0 ] && command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then \
		echo "  gh CLI is logged in on this machine. The cluster cannot reach it, but the"; \
		echo "  token behind that session can be stored as a Secret. You still approve it"; \
		echo "  inside the app before anything uses it."; \
		read -r -p "  Forward your gh CLI token into the cluster? [y/N] " reply; \
		case "$$reply" in [yY]*) token="$$(gh auth token)";; *) echo "  not forwarding; sign in with the OAuth device flow instead";; esac; \
	fi; \
	kubectl create namespace $(NS) --dry-run=client -o yaml | kubectl apply -f - >/dev/null; \
	kubectl -n $(NS) create secret generic $(K8S_SECRET) \
		--from-literal=gh-token="$$token" \
		--from-literal=github-client-id="$${GITHUB_CLIENT_ID:-}" \
		--dry-run=client -o yaml | kubectl apply -f - >/dev/null; \
	echo "  secret $(K8S_SECRET) applied"

.PHONY: k8s-tunnel
k8s-tunnel: ## (Re)start the background port-forward
	@$(MAKE) --no-print-directory k8s-untunnel
	@nohup kubectl -n $(NS) port-forward svc/yana-chan-4k $(PF_PORT):19080 >$(PF_LOG) 2>&1 & echo $$! >$(PF_PID)
	@echo "==> tunnel on :$(PF_PORT) (pid $$(cat $(PF_PID)))"
	@for i in $$(seq 1 30); do \
		curl -sf http://localhost:$(PF_PORT)/api/health >/dev/null 2>&1 && exit 0; \
		sleep 1; \
	done; \
	echo "tunnel did not come up; see $(PF_LOG)" >&2; exit 1

.PHONY: k8s-untunnel
k8s-untunnel: ## Stop the background port-forward
	@if [ -f $(PF_PID) ]; then kill $$(cat $(PF_PID)) 2>/dev/null || true; rm -f $(PF_PID); fi
	@pkill -f "port-forward svc/yana-chan-4k $(PF_PORT):19080" 2>/dev/null || true

.PHONY: k8s-open
k8s-open: ## Port-forward in the foreground (ctrl-c to stop)
	@echo "http://localhost:$(PF_PORT)  (ctrl-c to stop)"
	kubectl -n $(NS) port-forward svc/yana-chan-4k $(PF_PORT):19080

.PHONY: k8s-logs
k8s-logs: ## Follow the dashboard logs in the cluster
	kubectl -n $(NS) logs -f deploy/yana-chan-4k

.PHONY: k8s-restart
k8s-restart: ## Restart the pod, picking up a changed secret or image
	kubectl -n $(NS) rollout restart deploy/yana-chan-4k
	kubectl -n $(NS) rollout status deploy/yana-chan-4k --timeout=300s

.PHONY: k8s-status
k8s-status: ## Show what is running in the cluster
	@if kubectl get ns $(NS) >/dev/null 2>&1; then \
		kubectl -n $(NS) get pods,pvc,svc; \
		if [ -f $(PF_PID) ] && kill -0 $$(cat $(PF_PID)) 2>/dev/null; then \
			echo; echo "tunnel: http://localhost:$(PF_PORT) (pid $$(cat $(PF_PID)))"; \
		else \
			echo; echo "tunnel: not running -- start it with 'make k8s-tunnel'"; \
		fi; \
	else \
		echo "not deployed -- bring it up with 'make k8s-up'"; \
	fi

.PHONY: k8s-down
k8s-down: ## Tear the Kubernetes stack down (deletes the PVC and the stored session)
	@$(MAKE) --no-print-directory k8s-untunnel
	kubectl delete -k $(K8S_DIR)/overlays/$(OVERLAY) --ignore-not-found --wait=false
	@echo "==> waiting for the namespace to finish deleting"
	@kubectl wait --for=delete ns/$(NS) --timeout=180s 2>/dev/null || true
	@rm -f $(PF_LOG)
	@echo "yana-chan-4k removed. The cluster itself is still running."

.PHONY: k8s-validate
k8s-validate: ## Render and schema-check both overlays
	kubectl kustomize $(K8S_DIR)/overlays/dev  > /tmp/ghdash-dev.yaml
	kubectl kustomize $(K8S_DIR)/overlays/prod > /tmp/ghdash-prod.yaml
	docker run --rm -v /tmp:/work ghcr.io/yannh/kubeconform:latest \
	  -strict -summary -kubernetes-version 1.33.0 \
	  /work/ghdash-dev.yaml /work/ghdash-prod.yaml

# Kept as aliases: the tunnel is the only way in, so it has a short name.
.PHONY: tunnel
tunnel: k8s-tunnel ## Alias for k8s-tunnel

.PHONY: tunnel-down
tunnel-down: k8s-untunnel ## Alias for k8s-untunnel

# ----------------------------------------------------------------- valkey ---

# A throwaway Valkey on loopback for server mode and the Redis tests. The
# password is a placeholder for a container nobody else can reach, not a
# credential. Persistence is off so it never writes a token to disk.
VALKEY_IMAGE    ?= valkey/valkey:9.1.3-alpine@sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b
VALKEY_PORT     ?= 16379
VALKEY_PASSWORD ?= yana-dev-only

.PHONY: valkey-up
valkey-up: ## Start a throwaway Valkey on 127.0.0.1 for server mode and the Redis tests
	docker run -d --rm --name yana-valkey -p 127.0.0.1:$(VALKEY_PORT):6379 \
		$(VALKEY_IMAGE) valkey-server --requirepass $(VALKEY_PASSWORD) --save "" --appendonly no

.PHONY: valkey-down
valkey-down: ## Stop the throwaway Valkey
	-docker stop yana-valkey

.PHONY: test-redis
test-redis: ## Run the kv tests against the throwaway Valkey
	cd $(BACKEND) && GHDASH_TEST_REDIS_URL=redis://127.0.0.1:$(VALKEY_PORT)/0 \
		GHDASH_TEST_REDIS_PASSWORD=$(VALKEY_PASSWORD) go test -race -count=1 -v ./internal/kv/...

# ------------------------------------------------------------------- down ---

define stop_pid
	if [ -f $(RUN_DIR)/$(1).pid ]; then \
		pid=$$(cat $(RUN_DIR)/$(1).pid); \
		if kill -0 $$pid 2>/dev/null; then \
			pkill -P $$pid 2>/dev/null || true; \
			kill $$pid 2>/dev/null || true; \
			echo "stopped $(1) (pid $$pid)"; \
		else \
			echo "$(1) was not running"; \
		fi; \
		rm -f $(RUN_DIR)/$(1).pid; \
	else \
		echo "$(1) was not running"; \
	fi
endef

.PHONY: down
down: ## Stop everything running locally (dev servers, compose, tunnel, valkey)
	@$(call stop_pid,web)
	@$(call stop_pid,api)
	@$(MAKE) --no-print-directory k8s-untunnel
	@if command -v docker >/dev/null 2>&1; then docker stop yana-valkey >/dev/null 2>&1 || true; fi
	@if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then \
		cd $(ROOT)/deploy/docker && GHDASH_IMAGE=$(IMAGE) docker compose down --remove-orphans 2>/dev/null || true; \
	fi
	@echo "all stopped"

.PHONY: clean
clean: down ## Stop everything and remove build output
	@rm -rf $(ROOT)/bin $(RUN_DIR) $(FRONTEND)/dist $(PF_LOG)
	@rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@printf '%s\n' \
		'<!doctype html>' \
		'<html lang="en">' \
		'  <head><meta charset="utf-8"><title>Yana-chan 4K</title></head>' \
		'  <body>' \
		'    <p>The frontend bundle has not been built yet. Run <code>make build-frontend</code>.</p>' \
		'  </body>' \
		'</html>' > $(EMBED_DIR)/index.html
	@echo "cleaned"
