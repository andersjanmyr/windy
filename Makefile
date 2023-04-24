
.PHONY: deploy
deploy:
	fastly compute publish --token $FASTLY_ACCOUNT_SANDBOX

.PHONY: run
run:
	fastly compute serve

