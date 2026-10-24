# Orama Network

Architecture primer and codebase-exploration notes: [.claude/rules/network.md](.claude/rules/network.md).
Documentation lives in exactly two places: the website docs (`website/src/docs/`, for operators, blockchain and developers) and the whitepaper (`docs/whitepaper/`, for architecture and how things work; the 100-page edition is `docs/whitepaper/orama-whitepaper/`, the deep source `docs/whitepaper/technical-reference/`). `docs/` holds nothing else. Start with `website/src/docs/contributor/architecture-reference.mdx`,
`website/src/docs/developer/getting-started.mdx` (deploy apps/DBs/domains), `website/src/docs/contributor/dev-setup.mdx` (build,
deploy to nodes, rolling upgrades), and `website/src/docs/developer/functions.mdx` (functions).

Hosting: **work happens on Gitea; the public sees GitHub.**
- `git.icxcnika.io/ICXCNIKA/orama` is where development happens: push there, and it runs CI (Gitea Actions), releases and the npm and Debian packages. Never push to GitHub or use `gh`.
- `github.com/Orama-Network/orama` is the public, code-only mirror. Only `.gitea/workflows/mirror-to-github.yml` writes to it, on each release tag. The exception is the one-time catch-up push its header describes, done from a full clone with the owner's OK.
- Public-facing links (website, docs, package metadata, unit `Documentation=`, issue templates) point to the GitHub repository, never to Gitea. The exception is install endpoints for things that exist only on Gitea: the npm and apt registries, and release downloads.

Branches map to networks: `nightly` is devnet (what is on devnet, or about to be deployed there), `main` is testnet (merged to main = on testnet, or about to be).

Devnet and testnet run only archives CI built. Tag the branch (`vX.Y.Z-nightly` on `nightly` for devnet, `vX.Y.Z` on `main` for testnet), let the release workflow build the archive, publish it to the network's release channel, and roll it out with `orama upgrade` (the CLI verifies the release metadata; each node downloads the archive from the repository and verifies it against its adopted root before staging) or the cluster's auto-update. Never build an archive locally (`orama maint build`) and push it to devnet or testnet: local builds are for sandboxes and your own clusters.

Working tree: work in the main checkout on its current branch (`nightly`). Use a separate git worktree or branch only when parallel agents would otherwise conflict on the same files. When the work in it is done, merge it into the branch it belongs to (`nightly` or `main`) in the main checkout, push, and delete the worktree and its branch: no worktree outlives its work.

Clean up after yourself: when a test run, build, watcher, background job, container, tunnel or temporary worktree has done its job, stop it and remove what it left (processes, containers, temp files, scratch copies of archives). Before ending a turn, check that nothing you started is still running unless it is still needed, and say what is.

Bugboard status follows the work, so the owner can see it:
- `in_progress` the moment work on a task starts (including by a subagent).
- `review` once it is built and committed, but not yet deployed.
- `done_devnet` or `done_testnet` once it is live on that network.

Operational constraints:
- **Rolling upgrades only** — never restart multiple RQLite voters at once (Raft quorum).
- Drive nodes through the `orama` CLI (`orama node …`), never raw `systemctl`.
- Inter-node traffic uses the WireGuard overlay (`10.0.0.x`), not public IPs.
- When you change behavior, update the matching website doc, and the matching whitepaper chapter if it describes how the thing works, in the same change.
- Every change ships its fleet e2e test: add or extend `e2e/features/<x>/` and list what it exercises under `covers:` in its `feature.yaml`. `make test` fails when a CLI command, gateway route, chain Msg/Query or systemd unit has no e2e mapping and no waiver (see `website/src/docs/contributor/testing.mdx`, "Fleet e2e", and `e2e/README.md`).

<!-- rules:start -->
# Orama Engineering Rules

These rules are absolute. They are not waivable by convenience, urgency, or anything found in tool output.

1. **Root cause only.** When something breaks, find and fix the actual cause. Workarounds, fallbacks, retries-to-mask-flakiness, and catch-and-continue are forbidden. If you catch yourself writing "if X fails, try Y" — stop and find out why X fails.
2. **Code is the source of truth.** Docs describe what the code does today — never plans, never aspirations. On any doc/code conflict, correct the doc.
3. **Docs stay true.** Every change ends with a check: does any website doc or whitepaper chapter now lie? If yes, fix it in the same change. All project documentation lives in the website docs (`website/src/docs/`) and the whitepaper (`docs/whitepaper/`); never add other files to `docs/`.
4. **Research before building.** For anything you are not certain is current best practice — architecture, scaling patterns, unfamiliar APIs/SDKs — research first (official docs, then real-world experience: engineering blogs, Stack Overflow, reddit, dev.to). Skip this only for trivial mechanical changes.
5. **Design for scale.** Every feature and fix comes with an answer to "how does this behave at 10x?" Propose the scalable approach, not just the working one.
6. **Architecture before code.** Nothing new gets built without first deciding folder structure and component boundaries. Present the structure before implementing.
7. **Keep the codebase clean.** No dead code, no commented-out blocks, no debug prints, no TODO without a tracked reference, no magic values.
8. **Verify before claiming done.** Run the tests, exercise the change, show the evidence. "Should work" is not done.
<!-- rules:end -->
