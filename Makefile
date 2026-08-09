.PHONY: test security vulnerability upgrade-test ha-test fuzz chaos monitoring benchmark production-gate environment-contract verify-environment demo release verify-release

test:
	scripts/test-linux-runtime.sh

security:
	scripts/test-security.sh

vulnerability:
	scripts/test-vulnerabilities.sh

upgrade-test:
	scripts/test-upgrade.sh

ha-test:
	scripts/test-packaged-ha.sh

fuzz:
	scripts/test-fuzz.sh

chaos:
	scripts/test-chaos.sh

monitoring:
	scripts/test-monitoring.sh

benchmark:
	scripts/benchmark.sh

production-gate:
	scripts/production-gate.sh

environment-contract:
	scripts/test-environment-qualification.sh
	scripts/test-environment-evidence.sh

verify-environment:
	scripts/verify-environment-evidence.sh "$(EVIDENCE_DIR)"

demo:
	scripts/demo.sh

release:
	scripts/release.sh

verify-release:
	scripts/verify-release.sh dist
