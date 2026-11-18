CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0

.PHONY: generate test build image

# Regenerate the deepcopy code, the CRDs and the ClusterRole from the kubebuilder markers.
generate:
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd rbac:roleName=fortigate-operator paths=./... \
		output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

test:
	go vet ./...
	go test -race ./...

build:
	CGO_ENABLED=0 go build -o fortigate-operator .

image:
	podman build -t fortigate-operator -f Containerfile .
