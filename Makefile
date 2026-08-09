PLUGIN_NAME := play-again
WASM        := plugin.wasm
NDP         := $(PLUGIN_NAME).ndp

.PHONY: all build clean

all: $(NDP)

$(NDP): $(WASM) manifest.json
	zip -j $@ manifest.json $(WASM)

$(WASM): main.go go.mod
	WASMOPT=/opt/homebrew/bin/wasm-opt /usr/local/tinygo/bin/tinygo build -target wasip1 -buildmode=c-shared -o $(WASM) .

clean:
	rm -f $(WASM) $(NDP)
