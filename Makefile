.PHONY: test security fuzz chaos benchmark demo release verify-release

test:
	scripts/test-linux-runtime.sh

security:
	scripts/test-security.sh

fuzz:
	scripts/test-fuzz.sh

chaos:
	scripts/test-chaos.sh

benchmark:
	scripts/benchmark.sh

demo:
	scripts/demo.sh

release:
	scripts/release.sh

verify-release:
	scripts/verify-release.sh dist
