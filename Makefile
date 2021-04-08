
docs: godoc
	$GODOC

godoc:
ifeq (, $(shell which godoc))
	@{ \
	set -e ;\
	GODOC_TMP_DIR=$$(mktemp -d) ;\
	cd $$GODOC_TMP_DIR ;\
	go mod init tmp ;\
	go get golang.org/x/tools/cmd/godoc ;\
	rm -rf $$GODOC_TMP_DIR ;\
	}
GODOC=$(GOBIN)/godoc
else
GODOC=$(shell which godoc)
endif
