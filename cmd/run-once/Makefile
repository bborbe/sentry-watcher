include ../../Makefile.variables

# Override on the command line:
#   make run-once STAGE=dev
KAFKA_BROKERS   ?=
STAGE           ?= dev
CURSOR_PATH     ?= /tmp/cursor.json

run-once:
	@go run -mod=mod . \
		-kafka-brokers="$(KAFKA_BROKERS)" \
		-stage=$(STAGE) \
		-cursor-path="$(CURSOR_PATH)" \
		-v=2
