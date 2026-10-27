# Chain architecture

> **At a glance.**
>
> - **What:** `oramad` is the Orama L1: a Cosmos SDK v0.54.4 application on CometBFT v0.39.4, wired by hand in one Go module (`chain/`) that `core/` never imports. It runs 21 stores of stock SDK modules and Orama's own (`x/emission`, `x/fees`, `x/power`, `x/token`, `x/nodes`, `x/storage`, `x/archive`, `x/relay`, `x/houses`, `x/cnft`, `x/market`, `x/shielded`, `x/wasmpolicy`, plus wasmd's `x/wasm` when libwasmvm is linked). Four things set it apart from a stock chain: norama is public by default, with a shielded pool for private payments; the validator set comes from `x/power`, a blend of an equal-share bootstrap committee and capped stake, not from `x/staking`; ordinary transactions pay a burned base fee through `x/fees`; and a block must contain the transactions a 2/3 quorum of validators listed in their vote extensions. No governance module can change anything. A change to a stock module needs a new binary on a new genesis.
> - **Key numbers:** ports 31000 (P2P), 31001 (RPC), 31002 (gRPC), 31003 (REST), 31004 (Prometheus); 19 ante decorators; voting power scale 10^9; power cap 5% per operator (3% above 60 active operators, back at 5% after 30 epochs below 50); 30-epoch stake ramp; at most one third of voting power moves per block or per lambda step; bootstrap committee of at least 30 on a production chain-id; vote-extension list 32 KiB, 4 MiB embedded, 1,024 ante runs and 4,096 signature checks per block; the gateway serves 16 module queries, 8 simulations and 16 broadcasts at once; query gas limit 2,000,000.
> - **Code:** `chain/app/`, `chain/x/power/`, `chain/x/inclusion/`, `chain/x/confidential/`, `chain/client/`, `chain/cmd/`, `chain/native/`, `chain/proto/`, `chain/scripts/`, `core/pkg/chainread/`, `core/pkg/clusterreg/`, `core/pkg/gateway/handlers/chainread/`.
> - **Depends on:** [global nodes](37-global-nodes.md) for how `oramad` is installed and who runs it, [economics](40-economics.md) for emission, fees and tokens, [the gateway](../vol1/12-gateway-architecture.md) for the public route policy.

![The chain node, the clients that reach it and the paths between them](../diagrams/ch39-overview.svg)

## Why it exists

The private clusters of Orama carry tenant data. They do not carry value that strangers must agree about: who runs a global node, who owes whom for a storage deal, who may relay Tor traffic, how many norama exist. That needs a ledger no single operator controls, and the ledger has to fit the rest of the system. Five constraints shaped it.

First, the chain starts with no money and no stake. A proof-of-stake chain needs bonded value before it can pick validators, and there is no value before the first block. The answer is a bootstrap committee fixed in genesis, each seat with an equal share of voting power and no self-bond. Power then shifts, over a year or sooner, from the committee to stake-weighted validators under a handover factor that can only rise (`chain/x/power/`).

Second, a stake-weighted validator set concentrates. The code caps one validator's power share and redistributes the excess, rate-limits how fast power can move, and ramps new stake in over 30 epochs. Each rule exists because the review that wrote it found a way to take over the set without them (the comments in `chain/x/power/keeper/` name the findings).

Third, a payment between users is a choice, not a default of the chain. The bank module is kept, so `norama` moves publicly between users, and every protocol payout lands in an earnings ledger that its owner moves to a bank balance with `MsgWithdrawEarnings` when they choose. The private way to pay a person is the shielded pool. `orama chain send` and the SDK's `chain.transfer` make private the default and take an explicit `--public` or `public: true` to pay in the open, so a payment cannot become public by omission; they never fall back to a public payment when the private one cannot be built.

Fourth, a validator must not be able to censor for free. Vote extensions carry each validator's list of long-waiting mempool transactions, and a proposal that omits a valid listed transaction is rejected by every other validator (`chain/x/inclusion/`).

Fifth, there is no admin. The authority address of every stock module is the hash of a name that no module has, so every authority-gated message is unreachable. The rules change when validators run a different binary on a different genesis, and in no other way (`chain/app/app.go:UnreachableAuthority`).

## The model

**oramad.** The node binary. It starts CometBFT and the application in one process (`chain/cmd/oramad/`). Its home is `/var/lib/orama-global/chain` on a global node.

**Application.** `OramaApp`, a `baseapp.BaseApp` with the module manager, the ante handler and the ABCI++ handlers (`chain/app/app.go:OramaApp`). It is built by hand, not by depinject.

**Module.** A Cosmos SDK module: a keeper, messages, queries, genesis and optional begin and end blockers. Stock modules are wired unchanged except where this chapter says otherwise. The Orama modules are listed in [the module set](#the-module-set).

**Module account.** A bank account owned by a module, with explicit mint and burn permissions (`maccPerms`). Every module account address is a blocked address: no user can send to it.

**Authority.** The address that may sign a module's parameter-change message. For every stock module it is `UnreachableAuthority()`, the address of the module name `orama/no-authority`.

**Chain-id class.** A chain-id is a test network only when it matches one anchored pattern, `^orama-(([a-z][a-z0-9]*)-)?(localnet|devnet|testnet)-[a-z0-9]+(-[a-z0-9]+)*$` (`chain/app/params/chainid.go:ClassOfChainID`), whose optional name word may not itself be a class word: `orama-testnet-10`, `orama-devnet-e2e-run7`, `orama-pubnet-testnet-1` are test networks, `orama-devnet-localnet-1` is not. Anything else is production, including an id that only contains a class word such as `orama-1-testnet-x`, and an id of the retired class word `stagenet` such as `orama-stagenet-11`. Several checks (the faucet, the bootstrap stake, the short epochs, the small committee, the locked parameters, the optional shielded verifier) call that one function and read nothing else; the CLI keeps a copy of the pattern in `core/pkg/netclass`, and a test fails when the two differ.

**Bootstrap committee.** The genesis list of validators in `x/power`. Each has an equal bootstrap share B_i of 1/n and needs no stake. A seat counts only while its staking record is bonded, not jailed and not tombstoned.

**Power universe.** The committee seats plus every bonded, unjailed, untombstoned staking validator. These are the only candidates for CometBFT voting power.

**Lambda.** The handover factor in zero to one. Power is `(1 - lambda) B_i + lambda C_i`, where C_i is the stake-based capped share. Lambda only rises.

**Handover gate.** The count of simultaneously active validators that must be reached once before lambda may pass `pre_gate_lambda_cap` (0.95).

**Force-bond.** The rule that half of a committee member's reward is delegated to its own validator while lambda is below one, and cannot be withdrawn until then.

**Inclusion list.** A validator's vote extension: up to 32 KiB of raw transactions it has seen waiting for 10 seconds or more. **Extended commit:** the previous height's commit with those extensions, which the proposer injects as the first transaction of the next block.

**Locked genesis.** The check that a genesis file's module parameters equal the compiled defaults, for a chain-id class.

**Signer.** In `chain/client/tx`, anything that can produce a secp256k1 signature over sign bytes, a held key or an agent.

## How it works

### The application

`oramad` is a Cosmos SDK v0.54.4 application on CometBFT v0.39.4, with wasmd v0.70.3 and wasmvm v3.0.7 (`chain/go.mod`). The data backend is pebbledb, set in the default `app.toml`: the goleveldb backend of the pinned store library could not answer a versioned query for any height, including the latest (`chain/cmd/oramad/cmd/commands.go:initAppConfig`). The defaults the command writes are `minimum-gas-prices = 0.000001norama` (a local mempool floor, separate from the chain's base fee), `query-gas-limit = 2000000` and `iavl-cache-size = 100000`. A node refuses to start with a query gas limit of 0 on any chain-id that is not a localnet, because the public `/v1/chain/query/` route would otherwise let one query scan all state (`commands.go:guardStart`). The IAVL cache is held to about 60 MB because the stock size grew past half a gigabyte of live heap on 4 GB hosts that also run Kubo and the cluster.

The application sets `bApp.SetDisableBlockGasMeter(false)`. v0.54 disables the block gas meter by default, and without it the base fee would read zero gas used in every block and never rise (`chain/app/app.go`).

Blocks come about every 5 seconds when the validators are healthy. `oramad init` writes `timeout_commit = 5s` into `config.toml` from `chain/app/params/params.go:BlockIntervalSeconds` (`chain/cmd/oramad/cmd/commands.go:initCometBFTConfig`, tested by `TestInitCometBFTConfig_writesTheChainsBlockInterval`). The installer and the e2e deploy script edit no consensus timeout. Every block-counted time window is computed from the same constant ([Block interval and measured performance](#block-interval-and-measured-performance)).

#### Build variants

`chain/Makefile` builds the same `oramad` in several ways, selected by tags and cgo:

| Target | Tags | What it links |
|---|---|---|
| `build` | `nowasm`, cgo off | pure Go, for linux/amd64, darwin/amd64 and darwin/arm64; no CosmWasm, no Orchard verifier |
| `build-wasm` | none, cgo on | the host `libwasmvm` from the module cache (the target prints the download command when it is missing) |
| `build-linux-amd64-orchard` | `nowasm orchardffi netgo osusergo` | the Orchard verifier library, static against musl through zig |
| `build-linux-amd64-full` | `muslc orchardffi netgo osusergo` | one static library carrying libwasmvm and the Orchard verifier (`native-lib-linux-amd64`) plus the pinned out-of-process verifier binary |
| `build-linux-amd64-global` | `nowasm` | `orama-global` and `testnet-node`, pure Go |

A `nowasm` binary refuses to start a node that claims the wasm module, either in its options or in its genesis (`chain/app/wasm_novm.go:guardWasmClaim`). A binary without the Orchard library refuses to start a testnet or mainnet node; on a localnet or a scripted devnet it starts and accepts no shielded bundle (`chain/app/shielded_verifiers.go:verifiersOptional`).

The `chain/native/` crate exists because two Rust static libraries linked into one binary each carry their own copy of the standard library and collide. It builds libwasmvm and the Orchard verifier as rlibs of one crate and archives them once (`chain/native/Cargo.toml`). One crate, so one dependency graph and one version per semver range of each dependency: the two halves must agree on every crate they share. One crate of libwasmvm's tree is replaced: `cosmwasm-crypto` 3.0.9 comes from `chain/native/patches/cosmwasm-crypto` (`[patch.crates-io]`), a fork of upstream's release on the RustCrypto 0.14 curve crates (`k256`, `p256`, `elliptic-curve` 0.14, on `crypto-bigint` 0.7), because the 0.13 crates upstream pins are on `crypto-bigint` 0.5, whose constant-time operations are not (RUSTSEC-2026-0387). It changes only secp256k1 and secp256r1 verification and recovery, which take the 32-byte hash as a prehash instead of through an identity digest; every other file is upstream's, and its `Cargo.toml` says what changed and when the fork ends (a wasmvm release whose cosmwasm-crypto is on the 0.14 crates). `make -C chain native-crypto-test` runs upstream's secp256k1 and secp256r1 suites (Wycheproof, rootberg) on it and a differential test against the crates.io release (`chain/native/patches/cosmwasm-crypto-differential`): seeded real signatures with high-S twins, `r` and `s` at 0, 1, n-1, n, n+1, x-reduced recovery ids, keys off the curve, hybrid and truncated keys and wrong lengths, where every verify and recovery must return the same value or the same error code. The Orchard half reads bundles with its own reader and not `zcash_primitives`, whose pre-release RustCrypto pins (`crypto-common` 0.2.0-rc.1, `digest` 0.11.0-pre.9) would not resolve beside the stable releases the 0.14 curve crates need ([the shielded pool](43-the-shielded-pool.md#the-two-verifiers)). `chain/native/build.sh` copies libwasmvm from the module cache at the version `chain/go.mod` pins, builds with a pinned Rust toolchain and zig for the C parts, and prints the SHA-256 of the archive. It writes nothing tracked, because `orama maint build` runs it and a release build leaves the checkout as it found it; the `record` mode writes the hash to `chain/native/libwasmvm_muslc.x86_64.a.sha256` and the `verify` mode fails if a rebuild hashes differently. The crate supplies `__rust_probestack` itself, because the unmangled symbol that wasmer needs is exported only mangled by current `compiler_builtins` (`chain/native/src/probestack.rs`); the host libwasmvm that `make test-race` builds (`wasmvm-lib` in `chain/ci.mk`) gets the same file on Linux, so the race tests link the same routine.

### The module set

The store keys, in `NewOramaApp`, are: `auth`, `bank`, `staking`, `distribution`, `slashing`, `consensus`, `upgrade`, `feegrant`, `evidence`, `emission`, `power`, `fees`, `token`, `archive`, `nodes`, `houses`, `storage`, `relay`, `cnft`, `market`, `shielded`; `x/wasmpolicy` and wasmd's `wasm` mount their own stores in `installWasm`, and `shielded` has a transient store as well. `x/gov`, `x/mint`, `x/crisis`, `x/circuit`, `x/authz`, `x/group`, `x/nft`, `x/epochs`, IBC and an EVM are not wired.

| Module | Role | Described in |
|---|---|---|
| `auth`, `bank`, `staking`, `slashing`, `distribution`, `evidence`, `feegrant`, `consensus`, `upgrade`, `genutil` | stock, with the wrappers below | this chapter |
| `emission`, `fees`, `token` | supply, fees, factory denoms | [economics](40-economics.md) |
| `power` | CometBFT voting power and the bootstrap committee | this chapter |
| `nodes` | operators, nodes, role bonds, bindings | [global nodes](37-global-nodes.md) |
| `storage`, `archive`, `relay` | storage deals, the history registry, relay rewards | chapters 41, 42 and 46 |
| `houses`, `wasmpolicy`, `wasm` | governance, contract policy, contracts | chapter 44 |
| `cnft`, `market` | compressed NFTs and the market | chapter 45 |
| `shielded` | the Ironwood shielded pool | chapter 43 |

`x/inclusion` is a library, not a module: it has no store or messages, and the application calls it from the ABCI++ handlers. `x/confidential` is a library nothing imports (see [confidential nodes](#confidential-nodes)). `x/vpnlaunch` and `x/wasmbindings` are not wired as modules either; `wasmbindings` is installed into wasmd as a message handler and a query plugin.

The module accounts and their permissions (`maccPerms`): `fee_collector`, `distribution` and `power` hold none; `bonded_tokens_pool` and `not_bonded_tokens_pool` hold Burner and Staking; `emission` holds Minter; `fees`, `fees_deposits`, `nodes`, `houses`, `storage`, `storage_escrow`, `relay` and `shielded` hold Burner; `token` holds Minter and Burner; `archive`, `storage_archive`, `cnft` and `market` hold none; wasmd's `wasm` account exists when the VM is linked and holds no permission: a contract's `BankMsg::Burn` is routed to `x/token` (see `chain/x/wasmbindings/messenger.go`), so no contract can lower a bank supply behind a ledger. `BlockedAddresses()` is every one of them, so a user can never send to a module account.

### The block lifecycle

![One block: proposal, pre-blockers, begin-blockers, transactions, end-blockers, commit](../diagrams/ch39-block-lifecycle.svg)

The orders are set in `NewOramaApp` and each has a reason written beside it:

- **Pre-blockers:** `upgrade`, then `auth`.
- **Begin-blockers:** `emission` first, so that closing an epoch mints the validator share and hands it to `x/power` before anything else reads balances; then `storage`, `distribution`, `slashing`, `evidence`, `staking`, `genutil`.
- **End-blockers:** `bank`, `shielded`, `staking`, `nodes`, `houses`, `storage`, `relay`, `genutil`, `feegrant`, `power`, `fees`, `emission`. `staking` runs before `power` so that power sees the bonded set after this block's messages. `fees` advances the base fee once the block's gas is final. `emission` is last so that `ReconcileBurns` sees every burn of the block.
- **Genesis:** `auth`, `bank`, `distribution`, `staking`, `slashing`, `genutil`, `evidence`, `feegrant`, `upgrade`, `consensus`, `fees`, `emission`, `wasmpolicy` and `wasm` when present, `power`, `token`, `archive`, `nodes`, `cnft`, `market`, `houses`, `storage`, `relay`, `shielded`. `emission` follows `bank` so that a fresh genesis can see the supply; `power` is last because it records the emission epoch as its own genesis epoch and returns the genesis validator set.

CometBFT accepts validator updates from exactly one module per block. `stakingEndBlockOverride` wraps `x/staking` so that its end-blocker still matures unbonding and redelegation queues and updates pool accounting but returns no updates, and so that its `InitGenesis` returns none either (an exported genesis would otherwise carry updates from both staking and `x/power`, which the module manager rejects). `x/power`'s end-blocker is the only source of `ValidatorUpdate`s (`chain/app/staking_override.go:stakingEndBlockOverride`).

`OramaApp` overrides three BaseApp methods: `CheckTx` records accepted transactions in the inclusion seen-set; `FinalizeBlock` strips the injected commit; `Commit` lowers CometBFT's retain height to the one `x/archive` allows. BaseApp returns 0 (prune nothing) when pruning is off, and the override keeps 0; otherwise the height is `min(BaseApp's, archive's)`, and a non-positive archive height means nothing may be pruned. A block no archived range covers is therefore never pruned, however far the tip runs (`chain/app/retain.go:gateRetainHeight`).

### The ante chain

![The 19 decorators of an ordinary transaction, and the route of a signer-less shielded transfer](../diagrams/ch40-ante-chain.svg)

`setAnteHandler` builds the chain by hand, in the order of the stock chain except for four additions and one substitution (`chain/app/app.go:setAnteHandler`). In order:

1. `SetUpContext`, `ExtensionOptions`, `ValidateBasic`, the stock decorators.
2. The shielded `ShapeDecorator`: a shielded message must be the only message of its transaction, refused before any fee is taken.
3. `UploadSunset` (code upload is closed until the sunset height) and `DepositPayer` (records who pays a contract's state deposit).
4. `TxTimeoutHeight`, `ValidateMemo`, `ConsumeGasForTxSize`.
5. `x/fees`' `FeeDecorator` in place of the stock deduct-fee decorator ([economics](40-economics.md#settling-a-fee)).
6. `UndelegateGuard` and `MinDelegation` from `x/power`.
7. `SetPubKey`, `ValidateSigCount`, `SigGasConsume`, `SigVerification`.
8. The shielded `ProofDecorator`: proofs are checked after the signature, so unsigned garbage costs no proof work.
9. `IncrementSequence`.

A transaction whose only message is a `MsgShieldedTransfer` takes a different, shorter chain, because it has no signer and no declared fee: `SetUpContext`, `ExtensionOptions`, `TxTimeoutHeight` and the shielded `SignerlessDecorator`, which checks the bundle and the fee carried in its value balance (`chain/x/shielded/ante/ante.go:Route`). Every other transaction takes the ordinary chain.

`UndelegateGuard` refuses a `MsgUndelegate` or `MsgBeginRedelegate` that would take a bootstrap committee member's own self-bond below the amount `x/power` has force-bonded into it, while lambda is below one. It sums several withdrawals in one transaction before comparing (`chain/x/power/ante/undelegate_guard.go:UndelegateGuard`). `MinDelegationDecorator` simulates the transaction's delegation changes on a copy and refuses any that would leave a delegation strictly between zero and `min_delegation_for_rewards` (1 ORAMA), so the per-epoch reward walk cannot be filled with dust; a full exit is allowed (`chain/x/power/ante/min_delegation.go:MinDelegationDecorator`).

### The bank send restriction

One restriction is appended to the bank keeper's send path (`chain/app/app.go`): `TokenKeeper.SendRestriction`, which applies every factory token's pause, freeze, non-transferable flag, transfer fee and hook to any send of that token ([economics](40-economics.md#factory-tokens)). Norama has no send restriction of its own. A bank send of norama between users, from a contract to a user and from a user to a contract is a public payment, limited only by the sender's spendable balance. Every module account is a blocked address (`BlockedAddresses`), so no bank message can pay one: a public send can never reach the `fees` account that backs the earnings ledger or the `fees_deposits` account that backs the deposits, and the invariants that tie those ledgers to their accounts cannot be unbalanced from outside. Module keepers still move norama between module accounts and to plain accounts (the faucet, unbonding, `MsgWithdrawEarnings`). The private path is the shielded pool.

### Voting power

![From stake and committee seats to a CometBFT voting power](../diagrams/ch39-power-pipeline.svg)

`x/power` computes the voting power CometBFT sees, every block, in its end-blocker (`chain/x/power/abci.go:EndBlocker`). Everything in this section is deterministic fixed-point arithmetic over `math.LegacyDec`, with every collection walk done in a sorted order.

#### The universe

`computePowers` loads the bootstrap committee and checks each seat's eligibility (`committeeEligible`): its staking record must exist, be `Bonded`, not be jailed, and its consensus address must not be tombstoned. It then loads the bonded validators from `x/staking` by power. Each bonded validator that is not jailed and not tombstoned contributes its bonded tokens as stake. Only ed25519 consensus keys are accepted, and the chain's consensus parameters permit only that type.

The count of active validators is the number of distinct operators among bonded validators and eligible seats; a seat that also has stake is counted once.

#### The committee

A fresh genesis lists the committee through `oramad genesis add-bootstrap-validator`, one command per member with `--consensus-pubkey-file` pointing at the node's `priv_validator_key.json` and `--min-committee-size` (`chain/x/power/client/cli/genesis.go:AddBootstrapValidatorCmd`). `InitGenesis` gives each member a real staking validator record with status `Bonded` and zero tokens, calls the staking hooks that create the distribution and signing-info records, and returns the genesis validator set with equal power `PowerToCometBFT(1/n)`. The record is deliberately not entered in the power index of `x/staking`, so a seat with no stake never appears among the stake-indexed validators. A production chain-id must declare at least 30 members (`ProductionMinCommitteeSize`); a test chain-id may declare as few as `min_committee_size` (floor 1) (`chain/x/power/keeper/genesis.go:checkCommitteeSizeChainIDGate`).

#### Lambda

Lambda starts at 0, so P_i equals the bootstrap share and only committee members have power. Once per closed emission epoch the end-blocker recomputes it:

```
lambda = min(1, max(lambda_prev, bonded / bootstrap_exit_stake, epochs_since_genesis / bootstrap_deadline_epochs))
```

with `bootstrap_exit_stake` 271,000 ORAMA (about 5% of the first year's maximum emission) and a 365-epoch deadline (`chain/x/power/types/power.go:ComputeLambda`). It is monotonic by construction: `lambda_prev` is one of the terms. It reaches 1 at the deadline whatever the stake.

Three further rules hold it back:

- **The handover gate.** Until the number of active operators has once reached `2 * ceil(1 / cap)` (40 at the 5% cap, 68 at 3%), lambda is held at `pre_gate_lambda_cap` (0.95). The flag `gate_satisfied` is a one-way ratchet. Without it, a small set of validators could take all power the day the deadline passes, however little stake backed them (`HandoverGateThreshold`).
- **The step limit.** When lambda rises, the new value is scaled back until the total variation of normalised power it causes is at most one third. The check runs twice, once on the power the validators would publish (admitted, ramped stake) and once on the latent power (full stake), so a bond added just before a lambda step cannot be held back by the ramp and then released together (`chain/x/power/types/lambda_limit.go:ClampLambdaStepBoth`). The limiter bisects the interval between the old and new lambda 64 times.
- **Epoch cadence.** Lambda and the cap are advanced when the emission epoch is greater than `lambda_last_updated_epoch`; other blocks reuse the stored values.

Lambda cannot be set by any message. At lambda 1 a committee member keeps power only through its own stake, and a seat with none receives a zero-power update and leaves the set.

#### The cap

The cap is 5% (`cap_fraction_normal`) of the stake-based power, per operator. `UpdateCapState` steps it down to 3% (`cap_fraction_reduced`) when more than 60 operators are active, and steps it back up only after the count has stayed below 50 for 30 consecutive epochs (`chain/x/power/types/power.go:UpdateCapState`). The thresholds sit apart on purpose, so a count near 60 does not flip the cap every epoch.

#### Operators

A validator belongs to the operator of the live `x/nodes` node whose `consensus` binding holds its ed25519 consensus key (`chain/x/nodes/keeper/consensus_key.go:OperatorOfConsensusKey`, bound to `x/power` through `Keeper.WithOperators` in `chain/app/app.go`). The binding is signed by the consensus key for the operator, like any service key, so the operator has proved it controls the key CometBFT signs with. Every validator with no such node shares one operator, `unlinked`, so staying unregistered gains nothing. Retiring a node frees its keys, which unlinks its validators. `orama setup` sends the binding for the validator's node on a join and for every seat of a created network, signed by the consensus key on the node (`core/cmd/orama/internal/setup/onchain_consensus.go:bindConsensus`). The active count that steps the cap and opens the handover gate counts distinct operators.

`ComputeOperatorCappedShares` (`chain/x/power/types/operator_cap.go`) sums the effective (admitted) stake of each operator, runs `ComputeCappedShares` over the sums, and splits each operator's share over its validators in proportion to their stake, equally when they hold none. The result is aligned with the input and independent of its order. A validator therefore never holds more than its operator, and splitting an operator's stake over more validators changes nothing. The chain cannot prove that two operator accounts are one party, so the cap binds a declared operator, not a determined splitter.

#### Capped shares

`ComputeCappedShares` is a water-filling allocation over bonded stake (here over operators). Each validator's raw proportion is its stake over total stake. A validator whose proportional share of the remaining mass exceeds its ceiling is pinned at the ceiling, the excess is shared among the rest in proportion to their raw stake, and the pass repeats for at most n iterations until nobody is newly pinned. The ceiling is `min(cap, raw * max_redistribution_multiplier)` with the multiplier 2: a validator cannot receive more than twice its own raw proportion through redistribution, however much excess exists. Without that bound ten honest validators at the cap and ten attackers holding one unit each would hand the attackers half the power. Because of the bound the shares may sum to less than one; callers normalise by the actual sum. If `cap * n` is less than one (fewer than 20 operators at 5%), or total stake is zero, every validator gets an equal 1/n share (`ComputeCappedShares`).

#### The ramp

New stake does not count at once. For each validator `effectiveBond` splits the current bonded tokens into an admitted amount, whose ramp has finished, and an excess that began ramping at some epoch; the excess counts in proportion `elapsed / ramp_epochs` (30). Added stake starts a new excess; stake that leaves reduces the admitted amount first and at once. The published capped shares are computed from the admitted-plus-vested tokens only, and validators with nothing admitted are left out of that computation, because passing them in would hand each an equal share whenever the cap cannot bind (`chain/x/power/keeper/stake_ramp.go:effectiveBond`). A validator whose capped share falls to zero loses its ramp clock and admitted tokens, and a validator that unbonds fully has them pruned, so a later re-bond starts the ramp again.

#### The blend, normalisation and publication

For each entry the raw power is:

```
P_i = (1 - lambda) * B_i + lambda * C_i
```

where B_i is 1/n for an eligible seat and 0 otherwise, and C_i is the ramped capped share. The raw values may sum to less than one (during the ramp, with a jailed seat, with the redistribution bound), so rewards and CometBFT power are both computed on the normalised share `P_i / sum(P)` (`chain/x/power/keeper/power.go:normalizedShares`).

`limitPublishedIncreases` then compares the normalised shares with the previous block's published powers. If the total variation exceeds one third, the increases are scaled back by bisection while decreases apply in full, because keeping a jailed validator in the set to satisfy the bound would leave it voting. If the scaled result has no positive share and the desired set has one, the desired set is seated. A decrease that exceeds one third is still published (`chain/x/power/types/publish_limit.go:ClampPublishedIncreases`). The one-third bound is the light-client bound named in the code: no more than a third of the previous set's power may be replaced between two consecutive validator sets.

The comet power of an entry is `floor(share * comet_power_scale)`, with the scale 10^9, and at least 1 for any strictly positive share, because CometBFT reads zero as "remove this validator". An update is emitted only for entries whose power changed, and a zero-power update is sent for any operator that had power and is no longer in the universe, using the public key recorded when it was added. If the whole computation would leave no validator with positive power, the end-blocker returns an error instead of an empty set, which would halt the chain.

#### Force-bonding

The reward path of a committee member (described in [economics](40-economics.md#paying-the-validator-share)) delegates 50% of what its operator would receive to its own validator, up to a total self-bond of 2,000 ORAMA (twice `min_self_bond` of 1,000 ORAMA), and records the running total in `committee_self_bond`. `UndelegateGuard` makes that amount unwithdrawable until lambda reaches one. The purpose is to give each seat something to lose to slashing before it holds real power.

#### State

`x/power` owns these collections (`chain/x/power/keeper/keeper.go:Keeper`): `params`, `lambda`, `lambda_last_updated_epoch`, `gate_satisfied`, `cap_current_bps`, `cap_below_streak`, `genesis_epoch`, `bootstrap_committee`, `ramp_activation`, `ramp_admitted`, `ramp_excess`, `ramp_excess_epoch`, `last_power`, `last_pub_key` and `committee_self_bond`. Its module account holds no norama between blocks: it takes an epoch's validator share and pays it out inside the same call, and its invariant is that the balance is empty.

### Slashing on real stake

Stock `x/staking` slashes `TokensFromConsensusPower(power) * fraction`, taking `power` from what CometBFT reports. That assumes CometBFT power is proportional to bonded tokens at 10^6 norama per unit. Here it is on a different scale (capped, ramped, blended, times 10^9), so the stock conversion would burn about a thousand times too much, and the review that found this measured 83% to 100% of a validator's stake burned for one infraction instead of the 5% specified.

`slashBaseFix` wraps the staking keeper that `x/slashing` receives, and `x/evidence` reaches staking only through slashing. It ignores the power it is given and substitutes the power equivalent to the validator's current bonded tokens plus the initial balance of unbonding and redelegation entries created at or after the infraction height that have not matured, so the fraction applies to the stake that was at risk at the infraction (`chain/app/slash_base_fix.go:slashBaseFix`, `chain/app/slash_departed.go:slashableDepartedTokens`). The rounding loss is at most 999,999 norama.

The genesis defaults set the double-sign slash at 5% (the stock value), downtime at 0.01% (stock is 1%), the signed-blocks window at 10,000 and community tax at zero because no path can spend a community pool (`chain/app/genesis_overrides.go`). The other slashing parameters keep their stock defaults. A double-sign tombstones the validator. A tombstoned seat is out of the power universe for good.

A second wrapper makes bonding work for an outsider. Every account starts at zero and rewards arrive only as earnings, so `earningsFundedStaking` wraps the staking message server: before `MsgCreateValidator` or `MsgDelegate` runs, it tops the signer's bank balance up from its own earnings by the exact shortfall (`chain/app/staking_topup.go:earningsFundedStaking`). It runs inside the message handler, so a message that fails undoes the top-up with its cache branch.

### Inclusion lists

![Inclusion lists: extend, verify, propose, process, finalize](../diagrams/ch39-inclusion.svg)

A proposer could otherwise keep a transaction out of every block it proposes. The chain uses the vote extensions of ABCI++ to remove that option (`chain/app/inclusion_handlers.go`). The rule lives in the pure library `chain/x/inclusion/`; the app supplies the transaction format and the ante run.

**Switch.** Vote extensions are a consensus parameter: `abci.vote_extensions_enable_height`, set in genesis and never changed. From that height `ExtendVote` and `VerifyVoteExtension` act; from the next height the proposer injects the extended commit. Before it, and at the enable height itself, the handlers do nothing and the proposal handlers are the SDK defaults. The localnet default is 0 (off); `orama setup --create-network` sets it to 2 (`voteExtensionsEnableHeight`), and `InitChainer` refuses a devnet, testnet or production genesis that leaves it at 0 (see the consensus parameters below).

**The seen-set.** Each node keeps a bounded memory of transactions that passed `CheckTx` and when it first saw them (`chain/app/inclusion_pool.go:inclusionPool`). An entry is eligible after 10 seconds, expires after an hour, the set holds 16 MiB, and a transaction over 32 KiB is not tracked. It is local state, not consensus state; a `CheckTx` recheck failure removes the entry, and `FinalizeBlock` removes every included transaction.

**ExtendVote.** The validator picks the eligible transactions that are not already in its own proposal, sorts them lexicographically by raw bytes, and walks them: a transaction is chosen if it decodes with exactly one signer and one signature and is ordered, pays at least a base fee of 1, fits the per-extension and per-sender byte caps, and passes the full ante chain run against a scratch branch of the last committed state (`SelectList`). Chosen transactions apply to the scratch branch, so a later candidate is judged after them. The list is encoded with the magic `ORIL`, a version byte and length-prefixed entries. A node that cannot list sends an empty extension, which counts.

**Caps.** The per-extension cap is 32 KiB, cut so that a full set of `staking.max_validators` extensions, each with 2,048 bytes of vote overhead, fits half the block budget (the consensus block max bytes less a 2 MiB reserve). At the stock 100 validators the cap stays 32 KiB. A sender may fill at most one extension. The deduplicated bytes embedded with the extended commit are bounded by 4 MiB.

**VerifyVoteExtension.** A validator accepts an extension that decodes and passes the stateless `ValidateList` (strictly increasing order, no duplicates, size and per-sender caps, fee at least 1) and rejects anything else. It cannot check signatures, which need the sender's account number. Before the enable height a non-empty extension is rejected. The answer is always a verdict and never an error, because CometBFT panics the node when the application returns an error from this call: a listed transaction whose auth info states no fee, or a fee coin with no amount, decodes in the SDK but dereferences a nil when its fee is read, so `chain/app/inclusion_tx.go:statesAFee` refuses it before the fee is read, and `meta` turns any further panic of the SDK's types on a peer's bytes into a refusal of that transaction; the extension is rejected like any bad list. `chain/app/inclusion_nofee_test.go:TestInclusion_verifyVoteExtensionRefusesATxWhoseFeeCannotBeRead` holds the call to that rule.

**PrepareProposal.** From the height after the enable height, the proposer first checks the commit with `baseapp.ValidateVoteExtensions`, then injects the commit as the first transaction, prefixed with `ORAMA-INCLUSION-EXTENDED-COMMIT-V1:`. The prefix begins with a byte that cannot start a valid protobuf transaction, so no chain transaction can be mistaken for it. It then calls `Assemble`, which accepts only extensions of the right height and round with positive power, requires the valid ones to hold at least 2/3 of total power, and walks the deduplicated union in byte order. For each transaction it checks decode, fee, sequence (the next sequence of the sender, applied as it goes), the sender's byte budget and the block budget, then `Verify`, then `Admit`. Transactions that pass are the required prefix. The rest of the block comes from the SDK default handler with the remaining space.

**Budgets.** The walk is bounded so that junk cannot make every validator do unbounded work: 1,024 ante-chain runs and 4,096 signature checks per block in total, divided evenly among the extensions that list anything (at least one each), each transaction charged to the listing extension with the most budget left. A validator that lists junk spends its own share. Signature verification (`Verify`) runs before the sender is charged, so a transaction that only names a sender it cannot sign for costs that sender nothing; the cost falls on whoever listed it.

**ProcessProposal.** Every validator decodes the injected commit (and rejects it if it is not the canonical encoding of what it decodes to, so a proposer cannot pad the block with unknown fields), validates the vote extensions, and re-runs the same walk. A block is rejected if a transaction that was valid and fit is missing, or appears after a transaction that is not the next required one. Listed transactions that were invalid or did not fit may be absent. The first same-sequence transaction in byte order wins.

**FinalizeBlock.** The app strips the injected commit before BaseApp sees the block, so it never reaches an ante handler, and puts back a successful empty result in its place, because CometBFT needs one result per transaction.

If the commit cannot be turned into a valid block, for example because valid extensions hold under 2/3 of power, `PrepareProposal` returns an error and BaseApp falls back to the raw request transactions, which `ProcessProposal` then rejects.

### Genesis, locked parameters and export

`InitChainer` guards the genesis (`guardWasmGenesis`), runs `ValidateLockedGenesis` and `ValidateConsensusParams`, records the module version map and runs `InitGenesis` for the modules in the order above. The locked-genesis check compares the `params` object of each locked module's genesis (`emission`, `fees`, `power`, `nodes`, `storage`, `relay`, `token`, `archive`, `houses`, `staking`, `slashing`, `distribution`, `shielded`, and the SDK's `auth` and `bank`, and wasmd's `wasm` when the VM is linked: the fee-shaping signature and byte prices, the signer and memo limits, `default_send_enabled`, and who may upload and instantiate code) and the wasmpolicy sunset and deposit parameters with the compiled defaults, key by key, numeric strings compared as decimals. A localnet locks nothing. A devnet or testnet may differ only in `emission.epoch_duration_seconds`, `min_blocks_per_epoch`, `allow_bootstrap_stake`, the four faucet parameters, and `power.min_committee_size`. A production chain-id may differ in nothing. `oramad genesis validate` runs the same check after the stock validation (`chain/app/locked_genesis.go:ValidateLockedGenesis`). The test `TestLockedGenesis_everyGenesisParameterHasARow` fails when a module gains a parameter that has no row.

The locked values of the stock modules are these. `staking`: bond denom `norama` (`sdk.DefaultBondDenom` is set in `chain/app/config.go`), unbonding time 1,814,400 s (21 days), `max_entries` 7 and `historical_entries` 10,000, all the SDK's own defaults. `slashing`: double-sign 5%, downtime 0.01%, a 10,000-block signed-blocks window, jail duration 600 s. `distribution`: `community_tax` 0, the deprecated `base_proposer_reward` and `bonus_proposer_reward` 0, `withdraw_addr_enabled` true. The `x/houses` timelocks are 14 days for parameters, 7 days for spends and 60 days for upgrades, each with a floor at its default ([governance and contracts](44-governance-and-contracts.md)).

The genesis tooling is `oramad genesis set-emission-params`, `add-bootstrap-validator` and `add-standard-contracts` beside the stock genutil commands. The chain's default consensus block gas is not set in the module defaults: `x/consensus` has no genesis state, so `chain/scripts/localnet/localnet.sh` and `orama setup --create-network` (`ApplyConsensusParams`) patch `consensus.params.block.max_gas` (100,000,000) and `abci.vote_extensions_enable_height` into the genesis JSON (`orama setup` also writes `block.max_bytes`, 22,020,096).

**Consensus parameters are checked at InitChain.** `ValidateLockedGenesis` reads only `app_state`, and five groups of consensus parameters outside it matter: three bound the work and the fees of every block, `block.max_gas` (-1 is unlimited, and the base fee and the shielded proof work depend on it), `block.max_bytes` and `abci.vote_extensions_enable_height` (0 switches the inclusion lists off); `validator.pub_key_types` decides which key types `MsgCreateValidator` accepts, and `x/power` reads every bonded validator's key as raw ed25519 and halts the chain from `EndBlock` on any other, so one secp256k1 validator would be a one-message halt; and the `evidence` parameters bound how old and how large evidence of a double sign may be. `InitChainer` therefore validates the consensus parameters of the request by chain-id class (`chain/app/consensus_params.go:ValidateConsensusParams`, `params.ClassOfChainID`), after the locked genesis check and with the same error shape: a localnet is free; a devnet or testnet needs a finite `max_gas` above zero, a `max_bytes` above zero and at most 22,020,096 (21 MiB, `ConsensusMaxBlockBytes`) a vote-extension height above zero and `ed25519` as the only validator key type (`pub_key_types` is exactly `["ed25519"]`, on every chain but a localnet), and may pick its own values within that; any other chain id, production included, is pinned to exactly the values `orama setup` writes: `max_gas` 100,000,000, `max_bytes` 22,020,096, `vote_extensions_enable_height` 2, `ed25519` only, and the evidence window `max_age_num_blocks` 241,920 (x/archive's `DefaultBlocksIn14Days`), `max_age_duration` 14 days (that many blocks at the 5-second block interval) and `max_bytes` 1,048,576. The window equals the block retention on purpose: CometBFT verifies evidence against the blocks and validator sets a node still holds, nodes keep 14 days (`min-retain-blocks`, and `OramaApp.Commit` never returns a retain height above `tip - max_age_num_blocks` because BaseApp folds the evidence age into it), and 14 days stays under the 21-day unbonding time so a validator cannot unbond before its evidence expires; `TestConsensusEvidenceWindow_isWithinRetentionAndUnbonding` fails if the three drift apart. A genesis with no consensus parameters is refused on every class but a localnet. `orama setup` writes these values (`ApplyConsensusParams`) and `VerifyGenesis` checks them before the genesis is handed to a machine; `TestConsensusConstants_equalTheChainsPins` keeps the CLI's numbers equal to the chain's.

`oramad export` is the simapp export with the stock zero-height preparation. It is how a chain continues across a coordinated hard fork: `x/power` and `x/emission` both accept an exported genesis, trust its epoch and lambda state, and `x/power` rebuilds the validator set from the exported power records instead of recomputing equal shares.

### Authority and upgrades

`UnreachableAuthority()` is a function, not a variable, because it must be computed after the bech32 prefix is set; a package-level variable would capture the default prefix at static initialisation. It is the address of the module name `orama/no-authority`, which no module has, so no key or module account can sign for it. The test `TestUnreachableAuthority_rejectsEveryAuthorityGatedMsg` proves it for bank, staking, distribution, consensus and upgrade.

`x/houses` is registered and runs its end-blocker, but is not that authority. It reaches the rest of the chain through narrow adapters in `chain/app/enactment.go`: the emission split it enacted ([economics](40-economics.md#closing-an-epoch)); a change to the relay reporter set, delivered through `x/relay`'s own message path with a context flag that only this adapter sets; a software-upgrade plan, stored in `x/upgrade`; and the code-upload allow list read by `x/wasmpolicy`.

No module sets a migration and every `ConsensusVersion` is 1. Governed upgrades have a registry, `chain/app/upgrades`: one file per plan exports an `Upgrade` (`Name`, `CreateHandler(Deps)` returning the `x/upgrade` handler, and `StoreUpgrades` for stores the plan adds, renames or deletes), and `registry.go` lists them. `NewOramaApp` calls `setUpgrades` (`chain/app/upgrades.go`), which refuses an invalid or duplicate name, registers each handler under its plan name with `SetUpgradeHandler`, and, when a registered plan has store upgrades and the node starts from the upgrade-info file cosmovisor leaves (not on a skipped height), installs `UpgradeStoreLoader` so the stores change at the plan's height. The registry ships empty: no governed upgrade has happened, and a network is created fresh at its first release. A scheduled plan whose name has no handler still halts the node at its height with "UPGRADE NEEDED" ([global nodes](37-global-nodes.md#cosmovisor-staging) describes the cosmovisor swap), and x/upgrade halts a node whose binary already has the handler of a plan that is pending but not yet due ("BINARY UPDATED BEFORE TRIGGER"), so the handler lives only in the binary cosmovisor starts at the height. The plan a release enacts is declared in `chain/app/upgrades/CURRENT` (embedded in the binary; blank or comment lines mean none), and `upgrades.CheckRelease` fails when it names a plan with no handler; `make -C chain upgrade-check` runs that, `core/scripts/release.sh` runs it before tagging, and `TestRelease_theDeclaredPlanHasARegisteredHandler`, `TestRegistry_hasNoDuplicateNames` and `TestUpgrades_aRegisteredHandlerRunsAtThePlansHeight` (a test-only plan run through x/upgrade end to end) cover it. A release that changes consensus adds its plan's file, its `CURRENT` line and the checksums in its `SoftwareUpgrade` proposal ([governance](44-governance-and-contracts.md)).

### CosmWasm wiring

When libwasmvm is linked, `installWasm` builds the wasmd keeper with the chain's rules (`chain/app/wasm_vm.go:installWasm`):

- Capabilities are `iterator`, `staking` and `cosmwasm_1_1` through `cosmwasm_2_2`; `stargate` and `ibc2` are omitted, so a contract cannot query arbitrary modules or speak IBC (`chain/app/wasm_caps.go:WasmCapabilities`). The channel, port and packet keepers handed to wasmd are all refusals, and a contract IBC message is rejected in the message handler.
- The coin transferrer is wrapped to let a contract pay the fee and deposit accounts. wasmd calls it only to fund a contract on instantiate or execute, before the contract is registered, so the funding path marks the recipient as a contract for the norama restriction.
- The wasm engine is wrapped by `depositEngine`: every call that changes stored bytes meters the store, and on success charges the growth or releases the shrinkage through the state-deposit ledger in `x/wasmpolicy`; a deposit that cannot be paid turns the call into a contract error (`chain/app/wasm_deposit.go:depositEngine`). A failing call is reverted by wasmd and charged nothing.
- The token transfer hook is bound here: a token that names a contract gets a sudo call with `{"transfer_hook":{denom, from, to, amount}}` (`chain/app/token_hook.go`).
- Orama's message handler and query plugin (`x/wasmbindings`) are installed as decorators.

### Shielded wiring

`buildShielded` opens the nullifier database under `<home>/data/shielded_nullifiers`, next to the application database, so resetting a node's data resets it too. It builds two verifiers, the Orchard library linked through cgo and a pinned binary run out of process, and warms both at construction so their verifying keys are not built inside a consensus handler. The binary is `--shielded-verifier` or `<home>/bin/orama-orchard-verifier`; the node refuses to start it if its SHA-256 differs from the value linked into `oramad` at build time, which no flag, setting or environment variable changes. A node without both accepts no bundle, and says so in its log. At start, a node whose nullifier database does not fold to the accumulator the committed state holds refuses to run (`checkShieldedStoreAtStart`), and the database is a state-sync snapshot extension so a synced node has it. The shielded module's own rules are in chapter 43.

### Confidential nodes

`chain/x/confidential/` is the software boundary for nodes that would prove, by a TEE attestation, that their operator cannot read the machine, and for a marketplace that would place tenants on them. It accepts nothing. `Accept` returns `ErrNoAttestation` for every report. `QuoteVerifier.Verify` parses a SEV-SNP report (a 0x4A0-byte structure, versions 2, 3 or 5, ECDSA P-384) or a TDX quote (version 4, ECDSA P-256) to read its claimed measurement and report data, then fails because the vendor root registry is empty by default, and fails again with `ErrVerifierNotLinked` even if a root were present, because no certificate-chain or signature verification exists. `VerifiedReport` has an unexported field, so no code outside the package can fabricate one. `RegisterNode` needs a verifier and checks that the report's measurement equals the expected one and that its report data equals a SHA-512 over a domain, the operator, the node id and the node key. `MarketplaceLive` is the constant false and every lease returns `ErrMarketplaceNotLive`. The package is imported by nothing in the application, and a test keeps it that way; the e2e feature `chain-waivers` asserts that the live chain has no such surface.

### The Go client

`chain/client/` is the client library the global services link. It is separate from the node: its codec registers the SDK standard interfaces, auth, `x/storage` and `x/archive`, and any other module a caller registers, so a service does not link the application (`chain/client/node/encoding.go`).

`chain/client/node` talks to one `oramad` over its CometBFT RPC. `Query` runs a gRPC query method as an `abci_query`, at the latest height or at a given one; a non-zero ABCI code is a `QueryError`, with `NotFound` for the SDK's key-not-found. `Submit` signs the messages, simulates them for gas, scales the gas by 150%, pays exactly the current base fee for that gas with no tip, broadcasts with `broadcast_tx_sync` and polls for inclusion every second for up to a minute, returning `ErrNotIncluded` on timeout (the caller retries with a fresh sequence). `SubmitSignerless` broadcasts a message with no signature and no fee, for `MsgShieldedTransfer` alone. `FeeFunds` sums an address's bank balance and fee-only balance, because a node's hot key has only the latter.

`chain/client/tx` builds and verifies `SIGN_MODE_DIRECT` transactions. The account key is the 32-byte secp256k1 leaf itself, the BIP-44 leaf at coin type 118; `DeriveAccount` rejects a value outside `[1, N-1]` rather than letting the curve library reduce it into a different account. A transaction has exactly one signer, a fee in norama only and at least one message. `Decode` verifies a transaction's signature against a chain id and account number and returns the signer, fee and message type URLs. A `Signer` interface lets a key held by an agent sign without being read into the process. The fixtures `chain/client/tx/testdata/tx_vectors.json` and `wallet_msgs.json` are the cross-language contract with the TypeScript builder in `sdk/src/chain`: the TypeScript tests must reproduce the exact bytes.

### The binaries

- `oramad` (`chain/cmd/oramad/`): the node, the genesis commands, query and tx helpers, and the shielded-verifier flags.
- `orama-global` (`chain/cmd/orama-global/`): the services beside the chain, each its own subcommand and systemd unit, and each reaching the chain only through the loopback RPC (`tcp://127.0.0.1:31001`): `provider`, `repair`, `archiver`, `history`, `indexer` and `reporter`. A hot key file is created with mode 0600 and refused if any other user can read it. The services are described in [global nodes](37-global-nodes.md) and the chapters on storage deals, the archive and relay rewards.
- `orchard-smoke` (`chain/cmd/orchard-smoke/`): proves a static build on the machine that will run it. It checks that the linked Orchard verifier accepts the two committed vectors and rejects a tampered copy of each, and exits 0, 1 (a check failed) or 2 (built without the Rust verifier).

### How `core` reads and writes the chain

`core/` links no chain or Cosmos code. Three packages give it a way in.

![A wallet simulating, broadcasting and polling through the gateway](../diagrams/ch39-clients.svg)

**`core/pkg/chainread`.** The reader behind `orama chain`. It has three read paths: the gateway's `/v1/chain/` proxy, a node's REST API (`--node`) and a node's CometBFT RPC (`--rpc`). Orama module queries use embedded protobuf descriptors (`queries.binpb`, generated by `gen.sh` from `chain/proto` and the SDK and wasmd versions `chain/go.mod` pins), so the CLI can encode a JSON request and decode the answer without a generated type. `Simulate` and `Broadcast` post a signed `TxRaw` to the gateway and turn an HTTP 422 into a `TxRefusedError`. Responses are limited to 4 MiB and requests time out at 15 seconds.

**`core/pkg/gateway/handlers/chainread`.** The `/v1/chain/` proxy. It is a public route, with no credential, because a wallet has none and the chain charges the sender for a transaction. It forwards nothing it did not build: each route has an allowlist entry that constructs one upstream URL, and any other path, method or query is refused. The caller's path is cleaned and anything that `path.Clean` would change, a backslash or a percent sign is refused. Redirects from upstream are not followed.

| Route | Upstream |
|---|---|
| `GET /v1/chain/status`, `block`, `blocks`, `tx`, `validators` | CometBFT RPC; `blocks` spans at most 20 heights, `tx` is a 404 with `Retry-After: 2` until included |
| `GET /v1/chain/supply/norama`, `staking/pool`, `staking/validators` | the node's REST; the validator list is fixed at one page of 200 |
| `GET /v1/chain/index/...` | the indexer on 127.0.0.1:31015 (or the namespace address on a co-located host): status, stats, txs, blocks, accounts and their transactions, cNFT assets and owners; page at most 1,000, limit at most 100 |
| `GET /v1/chain/query/Service/Method` | `abci_query` of one allowlisted gRPC query |
| `POST /v1/chain/simulate` | `abci_query` of the SDK's Simulate service, answering gas and a fee at the current base fee |
| `POST /v1/chain/broadcast` | `broadcast_tx_sync`, answering code, codespace, log and hash |
| `POST /v1/chain/faucet` | none: the gateway signs `MsgFaucet` with its own key (`core/pkg/chainfaucet`), only on a test network, only where node.yaml `chain.faucet` is on; otherwise a 404 |

An upstream failure is never copied, because the node's message can carry paths and store details. A 404, or a CometBFT "not found" error, is answered `404 not found on chain`; any other error status, or a JSON-RPC error object under a 200, is `502 chain request failed`. CometBFT answers an error from a GET route, a transaction missing from its index included, with HTTP 500 and the error in the body, so the body is read whatever the status. Responses are copied unchanged up to 8 MiB (one block is the largest), except `/v1/chain/query/`, which decodes the answer. On the index routes the gateway checks an address's shape (lowercase `orama1` plus bech32 characters) and the indexer checks its checksum; a route that takes no query refuses one, and the validator list takes none.

The modules' own list queries (x/nodes `Nodes`, x/storage `Deals`, x/relay `Relays`) take their page through `chain/x/internal/pagebound/pagebound.go:Clamp`: the limit is held to the module's maximum of 100, no total is counted (a count walks every key) and an offset is refused in favour of the page key the previous page returned, so no public list can walk a whole map. The query allowlist is explicit: `publicQuery` names each Orama method served, `withheldQuery` names those that never are (every `Invariants` query, and the two that scan an unbounded set, `houses.Tiers` and `shielded.Pools`), and a test fails for an embedded method in neither list, so a new module query cannot become public by being embedded. `walletQuery` allows the bounded SDK reads a wallet needs: bank balance, all and spendable balances, the account, a delegation and a delegator's delegations and unbondings, a validator, the staking pool and parameters, distribution rewards, and wasmd's `ContractInfo`. `explorerQuery` (`query_explorer.go`) adds the reads of the explorer's token and contract pages: the bank's `TotalSupply` and `DenomOwners` and wasmd's `Codes`, `CodeInfo`, `ContractsByCode`, `ContractsByCreator`, `ContractHistory` and `AllContractState`, each paginated one held to a page of 100. wasmd's `SmartContractState` (it runs the contract, and wasmd does not mark it safe for a public query) and `Code` (the whole binary) are not served, and a code id that is not stored (`CodeInfo`) or an address that is no contract (`AllContractState`) is a 404 like `ContractInfo`'s (`wasmNotFoundPhrase` in `core/pkg/chainread/grpc.go`). A request is at most 4 KiB; a height must be within the last 100 blocks; a page limit above 100 is refused; the node's own `query-gas-limit` bounds each query; at most 16 run at once, then `503 Retry-After`. The gateway also gives the query route its own bucket per address (120 a minute, burst 30), simulate 30 a minute with burst 10 per address and 1,200 with burst 200 for the route, and broadcast 12 a minute with burst 4 per address and 600 with burst 100 for the route. The light-client route `POST /v1/chain/light` forwards only `status`, `commit`, `validators` and `consensus_params`, and answers `status` without `node_info.listen_addr` and `node_info.other.rpc_address`, which name the node's internal listeners (`handlers/chainread/light.go:withoutNodeAddresses`). A gateway that holds a faucet key also serves `POST /v1/chain/faucet` (`handlers/chainread/faucet.go`), with 3 a minute and a burst of 3 per address and 20 with burst 6 for the route, the address being `clientkey.Public`: the last `X-Forwarded-For` entry Caddy appended, believed only on a request that carries Caddy's proof header (`X-Orama-Caddy-Proof`, a per-node secret a process on the node cannot read), else the connection's address, so behind Caddy each public caller has a bucket and an allowance of its own and a process on the node is one client however it names itself); it signs `MsgFaucet` in process, one transaction at a time, only while the chain id is a test network's. A transaction is at most 1 MiB; simulate runs at most 8 at once and broadcast 16. The refusal of a transaction is answered 422 with a log that is made plain text first (invalid UTF-8 dropped, control characters turned into spaces, so no byte can split an address or a path out of reach of the patterns) and from which stack traces, source locations, filesystem paths and IP addresses are then removed, the length cut to 512 runes (`core/pkg/gateway/handlers/chainread/sanitize.go:sanitizeLog`). Upstream bases come from the environment (`ORAMA_CHAIN_RPC_URL`, `ORAMA_CHAIN_REST_URL`, `ORAMA_CHAIN_INDEX_URL`) and default to loopback, or to the `orama-global` namespace address on a co-located machine.

**Answers.** Simulate answers `{gas_wanted, gas_used, fee, base_fee}`: `gas_wanted` is the gas limit the transaction declares (`AuthInfo.fee.gas_limit`, 0 if it declares none; the SDK simulates with an unlimited meter, so its own `gas_wanted` is 2^64-1, which the proxy reads past), `gas_used` what it consumed, and `fee` the current base fee times `gas_used`. Broadcast uses `broadcast_tx_sync`, which answers after `CheckTx` and never waits for a block; the caller reads `GET /v1/chain/tx?hash=` (404 with `Retry-After` until the transaction is in a block). Every 64-bit integer the proxy, CometBFT and the module queries answer is a JSON string, as proto3 JSON has it (`gas_wanted`, `gas_used`, `account_number` and `sequence` included); the bare numbers are 32-bit (`code`, a transaction's `index`, `parts.total`, the JSON-RPC `id`). The indexer under `/v1/chain/index/` answers heights, gas and counts as bare numbers, because the chain bounds them far under 2^53. A gateway older than this answers `gas_wanted` and `gas_used` as bare numbers, which the current Go and TypeScript clients refuse (the TypeScript SDK throws `CHAIN_BAD_RESPONSE` rather than round), so gateways are upgraded before a client built on this SDK ships. An `account_number` is not a counter: x/auth assigns it as the first 8 bytes of a SHA-256 over the block height, app hash, tx index and address with the top bit forced to 1, so every account's number lies between 2^63 and 2^64-1 and a wallet must hold it as a `uint64`, never as a JavaScript `number`.

**Failures.** A refusal on either route is `422` with `{code, codespace, log}` (the `log` sanitised as above). Broadcast adds `tx_hash` and carries the transaction's own `CheckTx` code (13 insufficient fee, 32 account sequence mismatch); simulate has no hash because nothing was submitted, and its `code` is the wrapper's, always 6, so the reason is in `log`. Bytes the mempool already has are a 422 with code 19, log `tx already in mempool cache` and the hash. A full mempool is `503` with `Retry-After: 2`; an unreachable chain, or none answering within the gateway's 10 s, is `502 chain unreachable`; an answer the proxy cannot read (an unknown node error, a hash that is not 64 hex digits) is `502 chain request failed`; too many in flight is `503`; rate limiting is the gateway's `RATE_LIMITED` envelope, `429` with `Retry-After: 10`. A 200 from broadcast means the transaction is in the node's mempool, not in a block: a later block can make it invalid and a restart empties the mempool. A 502 after a timeout does not say the node did not take it, so the caller sends the same bytes again: the code 19 refusal with the hash says the mempool has them, and `GET /v1/chain/tx?hash=` is the only record of inclusion. The SDK raises `ChainTxRefusedError` for the 422.

**Module query answers** resolve every `google.protobuf.Any` (the account in `Account`, its `pub_key`, a validator's `consensus_pubkey`) from the embedded descriptors, which `gen.sh` extends with the account types (`cosmos.auth.v1beta1` base and module accounts, `cosmos.vesting.v1beta1`) and the key types (secp256k1, ed25519, secp256r1, bls12_381, multisig). An `Any` outside them cannot be encoded: the answer is `502 chain query failed`, and the gateway log has the method and the error that names the type URL.

**The index routes.** Each gateway asks the indexer of its own machine, so the indexer must run on every node whose gateway a client can reach. On a machine whose unit directory has no `orama-global-indexer.service` (a cluster node with no global services, or global services installed without `indexer`) a valid index route answers `503` with `Retry-After: 60` and the text `this node runs no chain indexer; install it with orama global install --services chain,indexer`, and does not call an address nothing listens on (on a co-located machine the namespace firewall drops it, which would be a ten-second wait). The check is made on every request, so an install is used without restarting the gateway, and it is skipped when `ORAMA_CHAIN_INDEX_URL` names an indexer. A path that is no route is still `404` and a wrong method `405`; an installed indexer that does not answer is `502 chain unreachable`.

**Freshness.** The proxy keeps no copy of a read: every `status`, `block`, `tx`, `validators`, index and query call is live against the node's RPC, REST or indexer, and every answer carries `Cache-Control: no-store`, so no browser, WebView or shared cache may keep it; Caddy on a node does not cache. The one reuse is the latest height the module-query `height=` window check reads, held for one second and never sent to a caller. `/v1/chain/status` answers from the node of the gateway that took the request, so two gateways can differ by the blocks between their nodes; `sync_info.latest_block_time` and `catching_up` say how fresh that node is. A height hours behind the chain is a node that fell behind or a copy kept by a client, not this proxy.

The module queries are also on each node's own REST API (`api.enable`, port 31003): every rpc of every Orama module's `Query` service carries a `google.api.http` GET annotation in `chain/proto/orama/<module>/v1/query.proto`, and the module's `RegisterGRPCGatewayRoutes` serves it at `/orama/<module>/v1/<method-in-kebab-case>`. A numeric key, an address or a node id is a path segment (`/orama/emission/v1/schedule-at/3`); a field that can hold a slash (a token denom, a deposit id) or is bytes is a query parameter. The node's REST API serves every query, each `Invariants` included; the public gateway serves only the allowlist. A path no route serves is answered by the REST server itself with HTTP 501 and `{"code":12,"message":"Not Implemented","details":[]}` (grpc-gateway v1 maps an unrouted path to gRPC `Unimplemented`), not a 404; a 404 means a routed query whose subject does not exist. `chain/app/rest_gateway_test.go` serves the routes over a real gRPC connection, and after a change to a `query.proto` the generated `query.pb.go` and `query.pb.gw.go` (`protoc-gen-gocosmos`, `protoc-gen-grpc-gateway` v1.16) and `queries.binpb` (`core/pkg/chainread/gen.sh`) are regenerated.

**`core/pkg/clusterreg`.** Hand-written protobuf for the messages the `orama global`, `orama storage` and `orama cluster` commands send: `MsgRegisterNode`, `MsgRegisterCluster`, bond, unbond, capacity and retire messages of `x/nodes`, the deal, extend, accept, decline, grant, revoke and proof messages of `x/storage`, and the stock unjail and edit-validator messages. Each type has a stateless validator that copies the chain's `ValidateBasic` limits (a test reads the chain's file so a change there fails here) and refuses an endpoint host that is an address in a range `core/pkg/netguard` lists, carries a zone id, ends in a number a resolver reads as an IPv4 address, or is a local name (`core/pkg/clusterreg/validate.go:rejectNonPublicHost`; the chain's `netclass.CheckHost` is stricter still, with single-label names and non-ASCII hosts, so the chain stays the judge), a bech32 check that is the same canonical-address rule as the chain's, and an encoder. `Direct` builds the `SignDoc` for `SIGN_MODE_DIRECT` and the final `TxRaw` from a 64-byte signature, so the CLI can print a document for an agent such as RootWallet to sign. `Broadcast` posts to the node's REST `/cosmos/tx/v1beta1/txs` in sync mode, and `WaitIncluded` polls the REST transaction endpoint every second for up to two minutes, reporting a failure in the block with the chain's log, control characters removed.

### Local and testnet scripts

`chain/scripts/localnet/localnet.sh` starts an N-validator localnet (default 4) on one machine, no Docker: ports step by 10 per node from 31000, chain-id `orama-localnet-1`, a 30-second epoch of 5 blocks, a block gas limit of 100,000,000, vote extensions off unless `VOTE_EXTENSIONS_ENABLE_HEIGHT` is set, every node a bootstrap committee member with zero self-bond, and optionally the wasm VM and the standard contracts (`WITH_WASM=1`). Three more switches serve the load generator: `WITH_SHIELDED=1` links the Orchard library into `oramad` and gives every node the out-of-process verifier binary, whose sha256 is linked in as the pin (a localnet built without them accepts no shielded bundle); `FAUCET_ENABLED=1` turns on the test-network faucet in genesis with no per-recipient cooldown; `TIMEOUT_COMMIT` patches `timeout_commit` in every node's `config.toml` (the default is 5 s). `localnet.sh funder-key` prints node 0's validator key, the seat that signs faucet drips. Supply starts at zero. `make -C chain bench-load` starts such a localnet, runs the load generator `chain/cmd/oramad-bench` against it and removes it (chapter 34, "The chain load generator").

`chain/scripts/testnet/` maintains the testnet machines; it no longer makes the network. A network is created, and its nodes joined, by `orama setup` (`orama setup --create-network` builds the genesis from every machine's chain keys, installs the signed release, starts the chains and registers the operator and the nodes; see [install and upgrade](../vol1/30-install-and-upgrade.md)). `deploy.sh` takes `reset` (back to a clean state before the next creation), `status`, `invariants`, `faucet` (on a created network: makes the public faucet's key on every node with `orama maint faucet init`, funds each faucet account with a `MsgFaucet` drip signed by that node's own seat key, writes `chain.faucet.enabled` into node.yaml and restarts the nodes one at a time with `orama node restart`), `smoke` and `gen-shielded`. `node/` builds `testnet-node`, a helper that runs on the node: a RootWallet-compatible signing agent for the operator key, `register-operator`, `fund-hot-key`, `epoch`, `earnings`, `tx-fee`. `smoke/` builds `testnetctl`, which runs the live checks over ssh into the namespace and prints PASS, FAIL or SKIP for each: blocks and inclusion lists, every module's invariants, the standard contracts, a private storage deal, archive ranges, shielded, and the gateway's chain read. The script refuses a chain-id that is not exactly `orama-[<name>-](testnet|devnet)-<id>`, and its operator key is a validator key in the test keyring, a devnet convenience.

`chain/spikes/` holds the measurements made before the design was fixed: the vote-extension byte budget, IAVL on disk, event shapes, the cNFT tree and the sign modes. Nothing in it is imported by the application.

`chain/proto/orama/` holds the protobuf sources of every module (`.../v1/` with `tx`, `query`, `genesis` and the module's own types). Generated Go and the REST gateway code live beside each module; the gateway's embedded descriptor set is regenerated by `core/pkg/chainread/gen.sh`.

## State it owns

| What | Where | Written by | Read by |
|---|---|---|---|
| Application state (IAVL, one store per module) | `<home>/data/application.db`, pebbledb | the module keepers, one block at a time | queries, other modules |
| CometBFT blocks, state, WAL, mempool | `<home>/data/`, `<home>/config/` | CometBFT | CometBFT, the indexer, the archiver |
| Shielded nullifier set | `<home>/data/shielded_nullifiers` | `x/shielded` | `x/shielded`, snapshots |
| `app.toml`, `config.toml` | `<home>/config/` | `oramad init`, the installer | the node |
| Validator key and sign state | `<home>/config/priv_validator_key.json`, `<home>/data/priv_validator_state.json` | `oramad init`, the sign floor ([global nodes](37-global-nodes.md#the-sign-floor)) | CometBFT |
| Genesis | `<home>/config/genesis.json` | the operator, once | the node |
| Seen-set of listed candidates | process memory, 16 MiB | `CheckTx` | `ExtendVote` |
| Power state | the `power` store, see [state](#state) | `x/power` | `x/power`, rewards, `UndelegateGuard` |
| Module accounts | bank | their modules | invariants |
| Embedded query descriptors | `core/pkg/chainread/queries.binpb` | `gen.sh` | the CLI and the gateway |
| Native library hash | `chain/native/libwasmvm_muslc.x86_64.a.sha256` | `native/build.sh record` | `native/build.sh verify` |

## Lifecycle

**Boot.** The installer places the `oramad` binary under cosmovisor and writes `app.toml`, which carries `query-gas-limit` ([global nodes](37-global-nodes.md#the-chain-unit)). `NewOramaApp` builds the keepers, opens the nullifier database, builds and warms the two shielded verifiers (stopping the process if a configured verifier will not start), and `LoadLatestVersion` loads the store. A node with a non-empty chain state also checks the nullifier database against the committed accumulator and panics on a mismatch. On a fresh chain `InitChain` runs the locked-genesis check and the module genesis.

**Normal operation.** Each height runs the lifecycle above. On a validator the work that is not consensus is the seen-set and the `ExtendVote` pass: an ante run per candidate on a scratch branch.

**Rolling upgrade and mixed versions.** There is no supported mixed-version state. The block lifecycle, the ante chain, the power computation, the inclusion walk and the fee market are consensus code; two binaries that differ in any of them disagree about an app hash and halt. A release that changes consensus code is a coordinated halt and, because no migration exists, a new genesis. The orchestration of node restarts for a release that changes no consensus code is in [global nodes](37-global-nodes.md#lifecycle).

**Restart.** The application state and the nullifier database must be restored together. State sync carries the nullifier database through a snapshot extension. The seen-set is empty after a restart, so a node lists nothing for its first 10 seconds.

**Node loss.** A validator that stops signs no blocks and is jailed for downtime after the signed-blocks window misses its threshold, losing 0.01% of its stake. With three equal bootstrap seats the chain halts rather than commit without one, because exactly 2/3 is not a quorum. A node that returns catches up from its peers. A key that signs at the same height on two hosts is a double-sign and loses 5% and the seat.

## Failure modes

| Trigger | What the system does | What you observe |
|---|---|---|
| Fewer than 2/3 of power online | CometBFT halts; no block until enough return | height stops; `catching_up` false on the online nodes |
| One of three equal committee seats offline | the chain halts; two equal thirds is not a quorum | the e2e destructive test relies on this |
| Validator double-signs | `x/evidence` slashes 5% of the stake at risk, jails and tombstones it; `x/power` drops its power at once | `ValidatorUpdate` to zero power in the next block |
| Validator misses the signed-blocks window | jailed, 0.01% slashed | validator `jailed: true`; its power falls to zero in the same block |
| Proposal omits a valid listed transaction | other validators reject the proposal | a nil prevote and a new round |
| Valid extensions hold under 2/3 of power | `PrepareProposal` errors; the fallback block is rejected | rounds without a block |
| Vote extension with a bad list | `VerifyVoteExtension` rejects it | the vote is not counted in the extended commit |
| Listed transaction invalid at execution | it fails like any transaction; the walk used the last committed state, not this block's | the transaction's code is non-zero in the block |
| Shielded verifier missing or failing to start | the node refuses to build the app, or accepts no bundle | `shielded verifiers` log line; `ErrVerifierNotLinked` |
| Nullifier database does not match the state | the node panics at start | message naming `shielded_nullifiers.db` |
| `query-gas-limit` is 0 on a non-localnet chain | `oramad start` refuses | the message gives the exact setting |
| Bad genesis parameters on a production chain-id | `InitChain` fails naming each differing key | the node never produces a block |
| A `nowasm` binary meets a wasm genesis | refuses to start | "built with -tags nowasm refuses to start a node that claims wasm" |
| Disk full | not handled by the application; the process fails as any CometBFT node does | the node stops; see the logs |
| Clock skew on one validator | BFT time is a median of validator timestamps; one skewed clock moves nothing; the seen-set's 10-second eligibility uses the local clock and is not consensus | a late or early list, never a different block |
| Slow peer | CometBFT consensus timeouts apply; the 16-query and 8-simulation limits protect the node from slow gateway clients | `503 Retry-After` from the gateway |
| Bad input to the gateway | refused before forwarding, with a fixed message | 400, 404, 405, 413 or 422 with a sanitised log |
| Gateway cannot reach the chain | `502 chain unreachable` | the explorer and wallets stop; the cluster is unaffected |

### Operator playbooks

No module has a pause, a freeze or an admin key, so every response to a chain fault is one of two things: reading the books, or the coordinated fix below. No external audit has been done, and none of the playbooks has been rehearsed on a live chain; each says where that matters.

#### Reading the books

Every module that holds or moves norama checks its own books through `oramad query <module> invariants`. `chain/scripts/testnet/deploy.sh` lists them in `INVARIANT_MODULES` (`emission fees storage nodes relay houses token market power shielded`), and `deploy.sh invariants` fails when any node reports a broken check.

**In-block conservation.** At the end of every block the chain asserts that nobody created or lost a coin (`chain/app/conservation.go`). After every module has finished its end-blocker it checks that the norama supply equals what `x/emission` recorded, which means no module but `x/emission` has minted norama and every drop has been booked as a burn, and that `cumulative_minted` is the schedule's. Every `min_blocks_per_epoch` blocks (14,400 on a production chain, about a day) it also runs a full pass: for every denom the supply is the sum of all balances, every denom with a supply is norama or a token `x/token` has a record of, the staking pools hold exactly the tokens of the validators and unbonding entries, and the ledger invariants of `x/token`, `x/power`, `x/fees`, `x/nodes`, `x/shielded` (without the nullifier-database fold), `x/houses`, `x/storage`, `x/market`, `x/relay` and `x/wasmpolicy` hold. A failed check is an error from the end-blocker, so the block does not commit and the chain halts on every validator at that height; `oramad export` runs the full pass first and refuses a state that fails it, unless the operator passes `--allow-conservation-violation`, which logs every violation at error level and exports anyway (the recovery path after a halt; see "A conservation halt" below). The checks read only committed state, in key order, with integers, so every validator reaches the same verdict. The full pass visits every balance once: about 0.7 microseconds per account in memory (`BenchmarkConservationFull`), so its cost grows linearly with accounts and the block that runs it takes that long. Tests: `chain/app/conservation_test.go` (each class of damage halts the next block), `chain/app/sim_test.go` (the seeded simulation, `make -C chain chain-sim`) and `TestModuleAccountPermissions_exactSet`, which pins the full set of module accounts and their Minter and Burner permissions.

| Module | What the check asserts |
|---|---|
| `x/emission` | minted equals the schedule; bank supply equals genesis supply plus validator, development and service mints minus burns |
| `x/fees` | earnings total equals the fees account; open deposits equal the deposits account; burned plus distributed equals collected |
| `x/storage` | escrow conserved; the module account holds exactly the queued mint payments; distinct operators; the reserved bytes `x/nodes` holds for each node equal the piece bytes of its replicas and fit its declared capacity; the settlement queue is well formed |
| `x/nodes` | module balance equals role bonds plus unbonding entries; active and capacity rules |
| `x/relay` | each settled epoch minted within its ceiling and its payouts sum to what was minted |
| `x/houses` | locked house bonds equal the houses account |
| `x/token` | each token's issued supply equals its bank supply; its metadata deposit matches `x/fees` |
| `x/market` | the market balance equals the open bids |
| `x/power` | the module account holds no norama |
| `x/shielded` | module balance equals pools plus the queued unshields; no negative pool; `data/shielded_nullifiers.db` folds to the accumulator in state |

`x/cnft` and `x/archive` hold no norama of their own, so they have no query. Only `x/emission` mints norama: `x/storage` and `x/relay` payments are minted by it and moved, and `x/token`'s bank keeper refuses a norama mint (`chain/app/mint_policy.go`, `TestGetMaccPerms_onlyEmissionMintsNorama`). The emission checks and the locked-parameter check also run on every `InitGenesis`, so a chain will not start from a genesis that breaks them. Run every query on every validator after any binary change and whenever a balance looks wrong. A broken invariant is a halt-height fix, never a patch on one node.

#### The coordinated halt-height fix

A fix to the state machine changes the app hash, so every validator must switch at the same height or the chain splits; nothing on chain switches them. The steps:

1. Keep the finding with the release signers until the fix is built; open no public issue before step 4 ends.
2. Build from a tagged commit with `make build` in `chain/`, using the `-trimpath` and version ldflags of the chain's `make build-linux-amd64-full`.
3. Sign the release so the TUF root verifies it (`orama maint global stage-oramad --release-metadata --release-target`, [build, signing and release](../vol1/29-build-signing-and-release.md)). The production signer ceremony has not happened; testnet uses a test root.
4. Announce a halt height in blocks, far enough ahead for every validator to act.
5. Each validator sets `halt-height` in `config/app.toml` or starts `oramad` with `--halt-height`. At that height the node commits the block and stops.
6. Each validator stages the new binary with `orama maint global stage-oramad --upgrade <name>` into the cosmovisor layout ([global nodes](37-global-nodes.md#cosmovisor-staging)). Validators stay on notify, so nothing installs by itself.
7. Blocks resume once validators holding more than two thirds of voting power run the fix, which before the lambda hand-over means the bootstrap committee.
8. Run every invariant query on every validator. The patched binary must run on the same home: it keeps `data/shielded_nullifiers.db`, which lives outside the application database, and the verifier binary `x/shielded` needs. A validator started from a copy of only `application.db` has an empty nullifier database, accepts a spent nullifier, diverges and fails the invariant.

The procedure swaps a binary over unchanged stored types. A fix that changes stored types has no migration to run (see Authority and upgrades) and restarts from an exported genesis, as in the committee-failure recovery below.

#### A conservation halt

The end-of-block conservation check (above) fails the block on every validator that computes it, so the chain stops at height H with the state of H-1 committed and every validator's log carrying `conservation violated at height H` and one line per failed check. Recovery:

1. Leave the validators stopped and compare the log line on at least two of them; the same checks at the same height mean the verdict is the chain's.
2. `oramad export` runs the same full pass and refuses the state with the same list. `oramad export --allow-conservation-violation --output-document exported.json` is the operator's explicit opt-in: it logs `EXPORTING A STATE THAT BREAKS CONSERVATION` and each violation at error level, then writes the genesis anyway. The flag is off by default. The file is evidence and a starting point, not a sound genesis. Every validator exports it, hashes it and compares.
3. If the checker was wrong, or the defect is in code that did not corrupt state, a patched `oramad` goes to every validator and they start together: block H was never committed, so it executes again under the patched binary. The patched binary has to compute the same state as the old one on every transaction (only the check changes); a fix that changes what a transaction does is the halt-height procedure above or a new chain.
4. If the state is damaged, the exported file is repaired by hand, validated with `oramad genesis validate`, and started as a new chain with a new chain id, as in the committee-failure recovery below. A validator started on the patched binary alone computes another verdict and forks itself off.

#### Bootstrap committee failure

Until lambda reaches 1 a permissioned committee of at least 30 seats signs blocks, and CometBFT needs more than two thirds of power online. Seen with `oramad status` (the height stops), `oramad query power bootstrap-committee` and `lambda` (a halt stops epochs, so lambda stops too), and CometBFT's `/validators`.

By itself the chain jails a member that misses more than half of a 10,000-block window for 600 seconds and slashes it 0.01%, and a jailed or tombstoned member's bootstrap share is zero at once, so a committee that keeps two thirds online repairs itself. Slashing and jailing need blocks: with less than two thirds online no block commits, nobody is jailed and nothing on chain can fix it. Seats lapse at lambda 1; lambda is held at 0.95 (`pre_gate_lambda_cap`) until enough validators are active, and the deadline is a genesis parameter no message extends.

Recovery when members are gone for good is a coordinated hard fork over exported state:

1. Wait for enough members to return if possible; the chain resumes by itself above two thirds.
2. Otherwise the survivors stop `oramad`, agree on one height and each run `oramad export --height H --output-document exported.json`. They hash and compare the files; a difference means state diverged and nobody proceeds.
3. `x/power` takes the validator set from the exported `power_records`, not from the committee list, so a seat that must stop signing has `comet_power` set to 0 there. A production chain-id refuses fewer than 30 committee members (`checkCommitteeSizeChainIDGate`), so the dead seats cannot be deleted from `bootstrap_committee`.
4. `oramad genesis validate exported.json` runs the module and locked-parameter checks. Every validator then starts the same file with the same fix binary and runs the invariant queries.

Not rehearsed: step 3's edit on a live chain, and whether a fork also needs a new chain-id. Not possible: adding a seat to a running chain, or shortening the deadline.

#### Directory authority or reporter compromise

`x/relay` pays from reports signed by a reporter set that is separate from the `DIRAUTH` role in `x/nodes`; nothing on chain links a reporter to a `DIRAUTH` node ([relay rewards](46-relay-rewards.md), [anonymity and Tor](38-anonymity-and-tor.md)). Compromise means a reporter's hot key, or an authority host's signing key and relay identity; the authority identity key is offline.

- **A compromised authority host** cannot mint certificates. Remove the authority from `tor-network.json`, ship the file in an emergency release, and run `orama maint global tor ceremony` for the replacement. The others keep voting; three authorities tolerate one loss and two lost lose the consensus. A host that is suspected, not compromised, gets a new signing certificate with `tor-gencert --reuse` on the offline machine.
- **A lying reporter** moves the median only below three reports; the default quorum is 3, so one liar is ignored. A genesis quorum of 2 makes the median the average of two values, and one bad reporter then skews a relay's weight by half the difference.
- **A reporter that is down or late** loses its epoch: reports for epoch `e` are taken only while the chain is in `e+1`, the epoch settles in the first block of `e+2`, and a late report is refused. With fewer than `min_reporters_quorum` complete reports the epoch mints nothing, so with three reporters and quorum 3 one reporter that stays down stops relay pay.
- **Replacing a reporter** is a structural proposal: `oramad tx houses submit-proposal --from <key> --content '{"relay_reporters":{"add":[...],"remove":[...]}}'`, voted with `vote-token` and `vote-operator`, run with `execute-proposal` after the timelock (`x/houses` has no hand-written transaction commands: `oramad tx houses` is generated from the module's Msg service, so each command's flags are its message's fields, see `oramad tx houses <command> --help`), through `x/relay`'s own `MsgUpdateReporters`. A change that would leave no reporter fails. Removal also applies to unsettled epochs, since settlement counts only reports from addresses in the set at that time. The structural tier must be open (lambda 1 and 21 eligible operators over 7 /16 networks and 5 ASNs) and the timelock is 60 days; before the tier opens, or when 60 days is too long, the only lever is a hard fork with a new reporter set in genesis.

Not possible: jailing a relay (`Keeper.JailRelay` has no caller), slashing another operator's `DIRAUTH` node (node messages are signed by the owner only), changing the quorum, caps or uptime minimum (genesis-only), or revoking a directory-authority certificate from the chain. Until a majority of authorities is independent, Orama could list only its own relays, and the VPN is not public until that is fixed.

#### Release-key compromise

A node verifies a staged `oramad` or archive against the TUF root at `/etc/orama/release-root.json` (`orama maint global stage-oramad` and `orama maint node stage-archive`, each with `--release-metadata <dir> --release-target <name>`). Validators are on notify and nothing installs a validator by itself. On a cluster node the auto-update agent (`orama maint node autoupdate run`) installs releases under that root when the cluster's `auto-update` setting is `auto`; on `notify`, the default, nothing installs without an operator. No code walks a chain of root versions: `releaseverify` loads the root it is given, so a new root is not checked against the old one, and there is no key revocation or threshold change inside a root. Replacing a root is a trust decision made out of band: `orama node trust add-root <file> --replace` on a node, or a build signed by the operator's wallet with `--release-root <file>`, which every node that installs it adopts (no build older than the last rotation can put an older root back). No command signs TUF metadata for a production root: that is the release signers' ceremony (`pkg/releasesign`), and `testtuf` signs with software keys for test networks only. The compromised keys' metadata stays valid for any node that still trusts the old root.

An operator can: (1) stop staging: set the cluster's `auto-update` to `off` (`orama maint cluster settings set auto-update off`) so no node installs from the old root, do not run `stage-oramad` or `stage-archive` for a release that cannot be verified out of band, and tell the other validators; (2) build a new root and targets on a trusted machine with new keys and reproducible builds; (3) adopt the new root on each node (`sudo orama node trust add-root <root.json> --replace`), or build the next archive with `--release-root <root.json>` and push it, after comparing the root's hash with the other operators over a channel that does not depend on the release repository; nothing verifies the new root against the old one, so this is the whole trust step, and replacing the root withdraws every earlier staged-release endorsement (`/etc/orama/release-staged.json`); (4) reset `release-seen.json` by hand if the new repository restarts numbering below the recorded snapshot version, since it refuses a lower one as a rollback (and `~/.orama/release-seen.json` on an operator machine that installs with `orama node setup --release`); no command does that; (5) re-stage the fix with the new root's metadata, as a halt-height fix if the compromise shipped a bad binary. The wallet-signer anchor for archives is a separate rotation that works: `orama maint build --signers 0xA,0xB` replaces the trusted list when a currently trusted signer signs it, retiring a key takes two builds, and a recorded build date stops replay of a retired key; the same replay rule covers a release root carried by a signed build (`--release-root`). It does not cover the chain binary.

#### Mass storage-provider failure

Seen with `oramad query storage queue`, `invariants`, `deal <id>` and `params`, the gRPC `Challenges` and `Slot` queries (no CLI), and the events `storage_slot_evicted`, `storage_slot_assigned`, `storage_slot_declined` and `storage_slot_accepted`. The chain's own response is in [storage deals](41-storage-deals.md): two consecutive misses slash 10% of the epoch price, four (`miss_threshold`) evict the slot, an evicted slot returns to `Pending` and is reassigned every block to a node with a distinct operator, /16 network and ASN, and an open deal with no holder is refunded. Below `s_min_providers` (8) distinct active operators the private-deal subsidy for the epoch is 0.

Operators and owners can: read a replica back with `orama storage get --deal-id N`, re-upload a slot to a new provider with `orama storage put --deal-id N --dir <seal output>`, run a repair delegate (`orama-global repair`, which rebuilds a lost slot from a surviving replica with the owner's repair seed and never sees plaintext), and let a provider that cannot serve answer `orama storage decline` (no penalty) or send `MsgReleaseReplica` (no slash, two per epoch, no CLI). Not possible: changing any storage parameter (genesis-only), or recovering a deal whose every replica is gone without the original `seal` output.

#### Settlement or archive backlog

The settlement queue drains `max_settlements_per_block` (100) items a block and each epoch adds one per challenge. `oramad query storage queue` gives `pending`, `head` and `tail`, and `invariants` checks `queue_well_formed` and `subsidy_within_ceiling`. Eviction, slashing and payment are all applied at settlement, so a backlog delays them, and a deal does not expire or return its escrow while an item is pending. Payment does not depend on the emission ceiling window, because `x/storage` mints the epoch's whole payment into its own account at epoch close; a queue lagging past 30 epochs still settles, except for items a binary older than that change wrote. A queue that grows faster than 100 a block needs a binary with a higher genesis-only rate, shipped as a halt-height fix.

For the archive, `oramad query archive last-archived-height` is the contiguous archived prefix (one unarchived range stalls it), with `range <start> <end>`, `retain-height` and `params`. Only canonical ranges of `range_blocks` are accepted, and an archiver that finds a root conflict writes `<home>/conflicts/<start>-<end>.json` ([archive and indexer](42-archive-and-indexer.md)). `orama-global history get --height H` verifies a bundle against its file hash, each block against its header and the root against `x/archive`. The retain height never rises above the last archived height, but no node prunes by it: a validator's real retention is its own `min-retain-blocks` in `app.toml`, which nothing sets. During an archive stall set it to 0 on every validator, by hand, until the stalled ranges are archived. Not possible: creating an `ARCHIVE` deal for a bundle from `x/storage`, or slashing an archiver for a wrong root.

#### Unshield run

`oramad query shielded pools` shows the pool balances and the queue, `tree-state` the tree and `invariants` the books. The brakes are all code and none is a switch: net unshield from a pool is capped at 2% of it (at least the 1 ORAMA floor) as a leaky bucket (half a cap, draining half a cap per 24 hours, so at most one cap in any 24 hours), and a bond unshield over the cap queues and is paid oldest first at most once per 24 hours; each vintage and asset has a turnstile so a pool cannot pay out more than went in; an unshield is signed by its target's owner and goes only to that owner's own bond, node bond or fee-only balance ([the shielded pool](43-the-shielded-pool.md)). There is no pause. A circuit bug is answered by the halt-height fix, with the cap bounding the loss meanwhile, and both verifiers call the same upstream `orchard` crate, so a soundness bug there is not caught by the second. A node that halts on the first shielded bundle after a restart is missing a verifier, has the wrong verifier binary (`--shielded-verifier`; the pin is linked into `oramad`) or lost `data/shielded_nullifiers.db`, and its start-up message says which. Not rehearsed: a shielded incident on testnet, and a state-sync of the nullifier database between two nodes.

## Trust and security

**Trust boundaries.** There are five. The validator set (stake plus the committee) is trusted for liveness and, above 2/3, for safety. A node operator is trusted with that node's keys only: the sign floor guards the validator key; a hot key signs a node's own transactions and holds only a fee-only balance. The gateway is a public, unauthenticated read and submit proxy that is trusted for nothing: it forwards signed bytes and answers reads the chain would answer to anyone. A client trusts a node's answers as it would any single full node (the library queries at latest height with no proof). The native verifiers are trusted for the shielded pool: two builds over one upstream implementation, each with its own bundle reader, must both accept a bundle.

**What an attacker can and cannot do.**

- *A user.* Can pay another user in norama in the open, or privately through the pool, but cannot pay a module account, cannot change any parameter, cannot mint, cannot make a transaction free (the base fee is consensus-enforced), cannot spend earnings except on fees, bonds, deposits and shielding, and cannot fill a block with listed transactions beyond the caps (32 KiB per sender per extension, 1,024 ante runs per block).
- *A proposer.* Can order and select transactions but cannot omit a valid listed one, cannot pad the injected commit with unknown fields, and cannot take another proposer's tips.
- *A validator holding up to a third of power.* Can stall but not finalise a conflicting block; the one-third publication bound means the validator set can never change by more than a third in a step, so a light client can follow it.
- *An operator with stake at the cap.* Is held to 5% (3%) of stake power after redistribution, however many validators it runs, and a stake added today counts fully only after 30 epochs. Splitting the stake across validators of one operator changes nothing, and each identity's redistribution gain is bounded by twice its own raw proportion. Splitting across operator accounts is not detected (the chain cannot tell them apart).
- *A committee member.* Has equal power and a force-bonded stake that cannot be withdrawn until lambda is 1, so double-signing costs real value.
- *A malicious gateway client.* Cannot reach a route that is not in the allowlist, cannot read an unbounded query, cannot learn paths or addresses from a refusal log, and is rate-limited per address and per route.
- *A package author or host.* A confidential node cannot register at all, because nothing verifies an attestation.

**Secrets.** The chain holds no secret in state. Keys live in files at `0600` on the node (the validator key and the hot key) or in a signing agent. The client builds and verifies transactions without ever holding an agent's key.

**What is not protected.** The chain is public: every block, transaction and ledger is readable. Privacy of payments comes only from the shielded pool, and a public transaction names its signer. Authority is not recoverable: there is no admin to rescue a stuck chain, only a coordinated binary and genesis.

## Limits and scale

**Hard numbers.**

| Quantity | Value | Source |
|---|---|---|
| block interval | 5 s `timeout_commit`, a little more per block | `chain/app/params/params.go:BlockIntervalSeconds`, written by `chain/cmd/oramad/cmd/commands.go:initCometBFTConfig` |
| consensus block max bytes | 22,020,096 (21 MiB), the CometBFT default | `chain/spikes/inclusion_test.go` |
| consensus block max gas | 100,000,000, patched into genesis by the scripts | `localnet.sh`, `orama setup --create-network` (`ApplyConsensusParams`) |
| `staking.max_validators` | 100 (stock) | the Cosmos SDK default |
| committee size | at least 30 on a production chain-id | `ProductionMinCommitteeSize` |
| lambda deadline, bootstrap exit stake | 365 epochs, 271,000 ORAMA | `DefaultParams` |
| cap, step-down, step-up, hysteresis | 5% / 3%, 60, 50, 30 epochs | `DefaultParams` |
| ramp | 30 epochs | `DefaultRampEpochs` |
| per-extension list, per-sender, embedded | 32 KiB, 32 KiB, 4 MiB | `inclusion.DefaultParams` |
| ante runs, signature checks per block | 1,024, 4,096 | `inclusion.DefaultParams` |
| query gas limit | 2,000,000 | `defaultQueryGasLimit` |
| iavl cache | 100,000 nodes | `defaultIAVLCacheSize` |
| gateway concurrency (query, simulate, broadcast) | 16, 8, 16 | `handlers/chainread` |
| transaction size | 1 MiB | CometBFT `max_tx_bytes`, which the chain does not change |

### Block interval and measured performance

The block interval is the `timeout_commit` of `config.toml`, `5s`, plus one round of votes. A parameter that is counted in blocks therefore lasts 5 s per block, and every such parameter is computed from `BlockIntervalSeconds`: the upload sunset (`chain/x/wasmpolicy/types/keys.go`, 3,162,240 blocks, 183 days), the anchor window (`chain/x/shielded/types/params.go`, 17,280 blocks, 24 hours) and the retention window (`chain/x/archive/types/constants.go`, 241,920 blocks, 14 days); tests pin each to its length in time, and `core/pkg/install` keeps `min-retain-blocks` equal to the retention window. The epoch needs 24 hours of block time and at least 14,400 blocks together, so at 5 s blocks the clock binds.

Speed is guarded by three measurements, none with a number in this book: the figures are the owner's run on the baseline machine.

- **Go microbenchmarks** in the `chain` module, run by `make bench` (`chain/Makefile`): the shielded verifier by action count for the in-process Rust library and for the out-of-process verifier, the nullifier store commit at 1M entries (10M with `BENCH_LARGE=1`), `x/power`'s end block at 30, 100 and 300 validators, the inclusion walk, `FinalizeBlock` and `CheckTx` on the app harness, an IAVL commit at 100k and 1M keys (10M with `BENCH_LARGE=1`) and a `cw20-base` call. `make bench-baseline` writes the baseline and refuses unless the machine is linux/amd64 with at least 8 CPUs; `make bench-compare BASE=<file>` runs the pinned `benchstat` and `chain/cmd/benchgate`, which fails when a benchmark's time per operation is more than 25% worse than the baseline's and benchstat does not call it noise, when a baseline benchmark has no result, when the input is empty or has fewer than 5 runs a side, or when the baseline is not a release baseline.
- **The fleet feature** `e2e/features/chain-perf/` records the interval between blocks over 40 blocks, the broadcast-to-block time of a public transfer (p50, p95, p99 over 30) and the CheckTx rate of a node (200 transfers back to back), timing them on the node against its loopback RPC. It also fails unless `timeout_commit` is `5s`. It writes `chain-perf/baseline.json` and, given the previous run's file as `E2E_CHAIN_BASELINE_FILE`, fails a p95 or a throughput more than `E2E_PERF_REGRESSION_PCT` percent (25 by default) worse, with the comparison code the `perf` feature shares (`e2e/features/internal/perfbase/`).

- **A state ten times the expected one, and a soak.** `chain/cmd/oramad-state` (a binary of its own, so no production `oramad` carries a generator that writes state without blocks) writes a large application state into a node home offline (`make state-gen`): 10M spent nullifiers through the nullifier database and the accumulator and count of `x/shielded`, 1M decompressed cNFT assets through `x/cnft`'s collections (compressed leaves are not state, a tree keeps a root), and 100M filler IAVL keys as `x/auth` accounts, two keys each. It is deterministic in a seed and resumable, committed in steps of one height each. To start a node on the result without blocks it rewrites the home's genesis (`initial_height` one past the last commit, that commit's `app_hash`, the validators `InitChain` returned) and saves CometBFT's state for it: CometBFT calls `InitChain` only for an application with no block and otherwise finds no state, so the handshake would pass and the node would stop. Its measurements, each printing JSON, run the real `oramad`: boot to the first block (`make state-boot`; the start-up check that the nullifier database folds to the accumulator in state is in it), snapshot create, archive, load and restore into a fresh home (`state-snapshot`; the restore is the application half of a state sync), genesis export (`state-export`, which builds the whole document in memory), the indexer's follower over a long history of a fast localnet (`state-indexer`, 500 blocks a step) and `BlockStore.PruneBlocks` of a synthetic block store at the height `x/archive`'s rule allows (`state-prune`). The fleet feature `e2e/features/chain-soak/` drives one public transfer every ten seconds for `E2E_CHAIN_SOAK` (24 to 72 hours for the owner's run) and samples every five minutes the height, whether each validator is catching up, the block and app hash at the height all have committed, the mempool, the anonymous memory, restarts and state size of the chain and the indexer, and the indexer's lag. The package is planned only when `E2E_CHAIN_SOAK` is set, so an ordinary run is unchanged. It stops at once on two validators storing different hashes for a block or a head that does not move for 15 minutes, and fails at the end on memory that only rises by more than 25% and 64 MiB after the warm-up, a restart, a validator or an indexer that stayed behind, or a mempool that did not drain (`e2e/harness/chainsoak`). It writes `chain-soak/timeline.json`. The commands, the hardware full scale needs and the limits of each figure are in `website/src/docs/contributor/performance.mdx`; no figure is in either document until the owner's run.

All three are part of cutting a release (`website/src/docs/contributor/performance.mdx`).

**At 10x.** The first bottleneck is the ante chain run inside consensus: `ExtendVote`, `PrepareProposal` and `ProcessProposal` each run it, up to 1,024 times, against scratch branches, and the signature checks add up to 4,096 more. The caps bound it per block, and the cost of exceeding them is that excess listed transactions are skipped, not refused, so a flood degrades the inclusion guarantee before it degrades liveness. Second is `x/power`: every block it walks the committee and every bonded validator, computes two water-filling passes (each at most n iterations over n validators) and, when it limits an increase, 64 bisection steps over all entries. That is cheap at 100 validators and grows quadratically in the number of validators at the worst case. Third, the epoch-closing block's reward walk ([economics](40-economics.md#limits-and-scale)). The `staking.max_validators` of 100 is the ceiling on the stake-indexed set; the committee is additional and fixed at genesis. The public gateway is bounded per address and per route, not per chain: ten times the wallets means more 429 and 503 answers, not more load on the node.

## Design decisions

### A bootstrap committee instead of a premine

*Chosen:* equal-power committee seats in genesis, zero stake, a handover factor that rises with time and stake. *Rejected:* a genesis allocation to the first validators. *Why:* there is no value to stake before the first block, and a premine would be a decision about who owns the network. The committee needs no money, and the force-bond gives it something to lose as it earns.

### Power from x/power, bonds from x/staking

*Chosen:* x/staking keeps bonds, delegation, slashing and jailing; its validator updates are discarded and x/power computes the set. *Rejected:* modifying x/staking's power function. *Why:* the SDK's modules stay unmodified and upgradeable in principle; the new rules sit in one module with its own tests. The cost is the slashing base fix, which exists because the two scales differ.

### The one-third publication bound

*Chosen:* limit total variation of published power to one third per block, increases scaled and decreases immediate. *Rejected:* publishing the computed power as is. *Why:* a light client skipping across validator-set changes needs at least a third of the older set to overlap the newer one. A jailed validator is removed at once anyway; a bound that held it would keep it voting.

### Two limits on lambda, one on published power

*Chosen:* bound the lambda step on both the published and the latent power; bound the published power per block separately. *Rejected:* bounding only published power. *Why:* the ramp hides stake from published power; a validator that bonded and waited for the ramp could then be released by a lambda step all at once. Checking latent power too closes that.

### Vote extensions for censorship resistance

*Chosen:* a quorum-attested list the proposer must include, in byte order, with a hard budget. *Rejected:* a longer commit timeout or a leader schedule. *Why:* inclusion is checked by every validator on the block itself, not by trusting a rotation, and a single honest validator's list reaches the block if it is within 2/3 of power.

### The injected commit as a transaction

*Chosen:* the proposer puts the previous extended commit in the block as its first transaction, with a magic prefix that cannot parse as a transaction. *Rejected:* a side channel. *Why:* ProcessProposal can then check the same bytes every validator sees. The cost is that `FinalizeBlock` has to strip it and put a result back, and that optimistic execution must stay off.

### One native library

*Chosen:* libwasmvm and the Orchard verifier in one Rust staticlib. *Rejected:* two staticlibs. *Why:* two copies of the Rust standard library in one binary collide at link time.

### Locked genesis by default values

*Chosen:* compare every genesis parameter with the compiled default by chain-id class. *Rejected:* a separate list of allowed values. *Why:* the default is the only source of truth; a new parameter with no row fails a test, so none can be forgotten.

### The gateway forwards nothing it did not build

*Chosen:* each route builds one upstream URL from validated parts. *Rejected:* a reverse proxy with a path allowlist. *Why:* the caller's path is never forwarded, so a path trick has nothing to reach.

## Known gaps

- A committee member's consensus key cannot be rotated under the same operator address. `x/power` prefers the staking validator's own consensus key once one exists and falls back to the genesis key; nothing detects a changed key, so the validator update keyed by operator address overwrites without ever emitting the old key's removal. Stock Cosmos chains cannot rotate consensus keys either (`chain/x/power/keeper/power.go`).
- `x/power.DistributeEpochRewards` walks every delegation of every validator once per closed epoch, rather than an O(1) per-block accumulator like stock `x/distribution`'s; it is O(delegations) a day by default, and `MinDelegationForRewards` (1 ORAMA) bounds how small a delegation can be (`chain/x/power/keeper/`).
- No upgrade handler or migration exists, so an `x/upgrade` plan halts the chain at its height but nothing applies it; every release that changes stored types restarts from a new genesis (`chain/app/app.go`).
- `x/confidential` accepts no attestation, has no vendor root and no certificate-chain verification, and is imported by nothing. Confidential nodes and the marketplace do not exist (`chain/x/confidential/verifier.go:QuoteVerifier`, `chain/x/confidential/listing.go:MarketplaceLive`).
- `useful_work_multiplier` and `useful_work_multiplier_activated` in `x/power` are validated and stored but read by nothing, so setting them changes no power or payout (`chain/x/power/types/params.go:Validate`).
- The per-operator power cap cannot tell two operator accounts apart: anyone can register another operator, node and consensus binding, so the cap bounds a declared operator and raises the cost of splitting but does not stop a determined splitter. A validator created with a consensus key that another operator's node already binds counts toward that operator until the node rebinds a new key (`chain/x/nodes/keeper/consensus_key.go:OperatorOfConsensusKey`).
- The `ValidatorPower` query returns the bootstrap, capped and power shares as zero and only the last CometBFT power and the operator (`chain/x/power/keeper/grpc_query.go:ValidatorPower`).
- The requirement that optimistic execution stay off, because it would skip the `FinalizeBlock` override that strips the injected commit, is a comment, not a check (`chain/app/inclusion_app.go:FinalizeBlock`).
- The inclusion walk judges required transactions on the last committed state, while they run after this block's begin-blockers; the base fee can move by 12.5% and rewards are paid in between, so a required transaction can fail at execution. The free space this allows is bounded by the embedded cap but not removed (`chain/x/inclusion/process.go:Required`).
- A comment in `chain/x/inclusion/params.go` sizes the embedded cap for a 150-validator active set; `staking.max_validators` is 100 and `x/power` does not change it.
- Vote extensions are a genesis-only switch; a chain that started without them cannot turn them on without a new genesis (`chain/app/inclusion_handlers.go:voteExtensionActive`).
- Disk-full and write-failure handling are not part of the application; they are whatever CometBFT and the store do.
- The conservation check halts the chain on a supply above the emission ledger and, once per epoch floor, on a broken ledger; a drop in supply is still booked as a burn (see [economics](40-economics.md#known-gaps)). The shielded nullifier-database fold is checked when a node starts, not in a block.
- `chain/x/confidential` and `chain/spikes/` carry no operational role; they are listed here because the manifest assigns them to this chapter.

## Verify it yourself

**Unit tests** (all in `make test`, from `chain/`):

- `chain/app/`: `TestOramaApp_buildsAndValidatesDefaultGenesis`, `TestLockedGenesis_initChainRefusesUnboundedBlockGasOnProductionChainID`, `TestValidateConsensusParams_productionPinsEveryValue`, `TestBankKeeper_userToUserNoramaSendIsAllowed`, `TestOramaApp_zeroSupplyGenesisProducesBlocksAndPaysEarnings`, `TestBlockedAddresses_coversEveryModuleAccount`, `TestUnreachableAuthority_rejectsEveryAuthorityGatedMsg`, `TestOramaApp_shieldedVerifiersAreTwoAndAnUnconfiguredNodeAcceptsNothing`, `TestLockedGenesis_defaultsEqualPlanValues`, `TestLockedGenesis_everyGenesisParameterHasARow`, `TestLockedGenesis_productionChainRejectsEveryChangedParameter`, `TestLockedGenesis_initChainRefusesChangedParameterOnProductionChainID`.
- Inclusion: `TestInclusion_listedTxMustBeInTheNextBlock`, `TestInclusion_proposalThatDropsAListedTxIsRejected`, `TestInclusion_badCommitIsRejected`, `TestInclusion_forgedSignatureIsNotRequired`, `TestInclusion_oversizedInjectedCommit`, `TestInclusion_disabledBehavesAsBefore` in `chain/app/`; `TestRequired_verifyAttemptsAreCapped`, `TestRequired_oneValidatorsJunkDoesNotStarveAnotherValidatorsTransaction`, `TestRequired_neverExceedsTheEmbeddedCap`, `TestAccept_authenticatedTrimsToTheEmbeddedCap`, `TestProcess_prefix`, `TestProcess_sameSequenceConflict` in `chain/x/inclusion/`.
- `chain/x/power/`: the power, lambda, cap, ramp, publication-limit and per-operator cap tests in `keeper/` and `types/` (`TestPowerShares_anOperatorWithManyValidatorsHoldsOneCap`, `TestComputeOperatorCappedShares_splittingStakeOverMoreValidatorsGainsNothing`, `TestRunEndBlock_aCapStepDownCountsOperatorsNotValidators`), and `TestApp_aValidatorCountsTowardTheOperatorOfTheNodeThatBindsItsConsensusKey` in `chain/app/`.
- `chain/client/tx`: `vectors_test.go` regenerates the cross-language vectors with `-update-tx-vectors`.
- `core/pkg/gateway/handlers/chainread/`: `TestQuery_everyEmbeddedMethodIsClassified` and the allowlist, sanitiser and limit tests; `core/pkg/clusterreg/` tests read the chain's limit constants.
- `chain/spikes/`: `TestVoteExtensionBudget` prints the byte budget at 30, 60, 100 and 150 validators.
- `chain/cmd/benchgate/`: the regression gate's parser and verdicts (`TestJudge_failsOnARegressionOverTheThreshold`, `TestJudge_aMissingBenchmarkFails`, `TestParseBenchstatCSV_emptyAndUnusableInput`) and the baseline-host rule (`TestNewStamp_refusesABaselineOffAStableHost`). The benchmarks themselves are `go test -bench` in the packages named above, run by `make bench`.
- `chain/cmd/oramad-state/`: the state generator's counts, determinism and resume (`TestRun_writesTheCountsOfThePlan`, `TestRun_isDeterministic`, `TestRun_resumesAfterAStopWithTheSameResult`, `TestKeysPerAccount_matchesTheStore`), the plan arithmetic (`TestPlan_shareCoversEveryIndexOnce`) and the measurements' helpers (`TestPrune_neverPastTheLastArchivedHeight`, `TestNodeWaitHeight_failsWhenTheNodeExits`). `e2e/harness/chainsoak/` (run by `make e2e-test-unit`): the soak's analysis (`TestAnalyze_divergence`, `TestAnalyze_stallWhenTheHeadStops`, `TestAnalyze_monotonicMemoryGrowthFails`, `TestAnalyze_aSawtoothThatDoesNotGrowPasses`).

**Fleet e2e** (the owner runs the fleet suite): `e2e/features/chain-core/` (consensus agreement, fee market, public transfers, unreachable authority, staking from earnings, every module's invariants), `e2e/features/chain-core-destructive/` (restart catch-up, base fee under load, export, the undelegate guard), `e2e/features/chain-economics/` (emission, power, nodes), `e2e/features/chain-waivers/` (unwired modules stay unwired; vote extensions enabled at exactly the genesis height), `e2e/features/chain-cli/`, `e2e/features/chain-explorer/` and `e2e/features/chain-wallet-routes/` (the gateway's chain routes). `e2e/features/chain-perf/` (the block interval, public transfer inclusion latency and CheckTx rate, with the baseline comparison). `e2e/features/chain-soak/` (opt-in with `E2E_CHAIN_SOAK`: steady load for a day or more, sampled every five minutes). `e2e/features/chain-chaos/` (stage 11, alone: a validator cut off from the others' p2p under a stream of public transfers, `kill -9` of the chain process under load, the shielded verifier child killed repeatedly, the chain home's filesystem filled until the chain's user cannot write, one validator's clock two minutes ahead and behind, and a wiped validator restoring a snapshot; each ends with the same blocks and app hash on every validator). The verifier and state-sync tests apply only to validators that run the verifier child or that the installer set up for state sync, which a run chain from chain-deploy.sh does not; a verification in flight is not reached, because only the Rust wallet builds a proven bundle.

**Read-only on a node or through the gateway:**

- `orama chain status` prints the chain id, height and sync state; `orama chain validator` the validator set; `curl http://127.0.0.1:31001/status` the node's own CometBFT status.
- `orama chain query orama.power.v1.Query/Lambda`, `.../Params` and `.../BootstrapCommittee` show the handover factor, the power parameters and the committee; `orama chain query orama.power.v1.Query/ValidatorPower '{"operator_address":"oramavaloper1..."}'` the last CometBFT power of one operator.
- `orama chain query --list` prints every query the CLI embeds; the gateway serves only the public subset under `/v1/chain/query/`.
- `curl -s http://127.0.0.1:31001/consensus_params` shows `block.max_gas` and `abci.vote_extensions_enable_height`.
- On a node, `oramad genesis validate` runs the locked-parameter check against a genesis file.
- `chain/cmd/orchard-smoke` run on the target host checks the shielded verifier before the node uses it.
