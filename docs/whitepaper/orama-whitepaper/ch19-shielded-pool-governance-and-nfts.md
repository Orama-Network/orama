# The shielded pool, governance and NFTs

> **At a glance.**
>
> - **What:** the private payment layer (`x/shielded`, Orchard notes with Halo 2 proofs), the governance and contract layer (`x/houses`, `x/wasmpolicy`, CosmWasm), and compressed NFTs with a marketplace (`x/cnft`, `x/market`). All three keep money out of the public graph and give nobody an administrative key.
> - **Key numbers:** bundle of 16 actions by default and 32 at most (53,325 and 103,823 bytes); two verifiers must agree; unshield cap per 24 h is the larger of 2% of the pool and 1 ORAMA; token quorum 40%, operator house of at least 21, timelocks 14, 7 and 60 days; contract state deposit 68,359 norama per byte; NFT tree depth up to 30, mint batch 64.
> - **Code:** `chain/x/shielded/`, `chain/x/houses/`, `chain/x/cnft/`.

## The shielded pool

A public chain leaks who pays whom. Orama lets a user choose. A bank send of norama between users is an ordinary public payment. The private way to pay a person is the pool, a single balance of norama held by the `shielded` module account and spent as Orchard-style notes. Inside the pool the chain sees nullifiers, note commitments, a value balance, an anchor and a proof. It never sees an amount, sender or recipient.

![The shielded pool: wallet, transactions, the admission pipeline, the two verifiers and the state](../technical-reference/diagrams/ch43-overview.svg)

Value enters with `MsgShield` or `MsgShieldEarnings`, moves with the signer-less `MsgShieldedTransfer`, and leaves with `MsgUnshield`. A transfer carries no signature, memo or fee payer; its value balance is its fee, and its gas limit must equal exactly 250,000 per action. A node only verifies proofs; the prover lives in a wallet crate never linked into `oramad`.

Admission runs cheapest first and changes no state (`chain/x/shielded/keeper/admit.go:Admit`). It parses the bundle, bounds the action count, checks the value rules, rejects any nullifier already spent or pending, requires an anchor within the last 14,400 blocks, and only then verifies proofs. Signed messages go through the normal ante chain, where proof checking runs after signature verification so unsigned garbage costs no proof work. The message server re-runs the full check at delivery whatever the ante handler did.

### Two verifiers, one fail-closed rule

A soundness bug in the circuit would be a counterfeiting bug, so every bundle must pass two verifiers: a cgo library over upstream `orchard` 0.15.5 and a separately built binary run as a child process, `orama-orchard-verifier`. They are two builds over one implementation, not two implementations: both do every cryptographic check with the same upstream crate, and only the code that reads the bundle's bytes differs (the library's own reader, `zcash_primitives`' in the binary). They protect against a fault in one build (memory corruption, a link problem, a hang) or a bug in one reader, and a logic bug in the shared upstream crate passes both. `verify.Check` (`chain/x/shielded/verify/verify.go:Check`) refuses to run unless two distinct verifiers are present, and every one must accept. A fault, such as a 30 second hang or a crash, kills the process and is never turned into an accept and never retried. A fault is not a verdict on the bundle, so in a block it records no result: the node stops with an error that names the verifier, and executes the same block again after the operator fixes it and restarts, so it cannot reach another verdict than its peers. A block proposal is checked before it is voted for: every shielded bundle in it goes through the mempool's verifiers, and a proposal with a bundle that is rejected or faults the verifier gets a nil vote, so a bundle that kills the out-of-process verifier makes the proposer rotate instead of halting the chain (a bundle that aborts the in-process library still stops every validator when the block executes). The node refuses to start a binary whose SHA-256 differs from the pin linked into `oramad` (the only source of the pin, with no flag to change it), because two nodes with different binaries could disagree about a bundle.

Go computes the signature hash, domain-separated and including the chain id, and hands it to both verifiers, so there is one definition and a bundle cannot replay on another network. An unshield additionally commits its signer and target into the hash. A copied bundle then fails verification, and a mempool observer cannot redirect the payout.

### Nullifiers, anchors and the cap

The note-commitment tree is a depth-32 Sinsemilla tree. The chain stores only its frontier and one root per block of the anchor window. The spent set cannot be pruned, since a nullifier proves nothing about age, so it lives in a dedicated database outside IAVL. The app hash commits to a running SHA-256 accumulator and a count. Records are visible only below the reader's height, which makes crash replay safe: a re-executed block first deletes its own records. At start a node folds the database and panics if it disagrees with the committed accumulator. State sync carries the records in a snapshot extension and checks that they fold to the accumulator.

Because the chain cannot detect counterfeiting, it bounds the loss. The pool balance is a turnstile: no outflow may exceed it. Net outflow, including proposer tips, is capped, as a leaky bucket that holds half of it and drains that half per 24 hours, so never more than the larger of 2% of the pool and 1 ORAMA. An unshield may target the signer's own delegation or node bond, or a fee top-up. A bond that does not fit the cap is queued and served at most once per 24 hours, oldest first and with a per-address ceiling, while a top-up that does not fit fails. Per node, a failed-bundle memory and a budget of 256 verifications between blocks limit proof-spam (garbage that fails its proof is free and can still use up the budget so that honest bundles are asked to retry; per-peer limits at the sentry nodes are the mitigation and an accepted gap), and at most 64 signer-less transfers fit in a block: a proposer leaves the surplus out and validators reject a proposal that holds more.

The module has no authority address, no pause and no parameter-change message. A defect is fixed only by a governed chain upgrade.

### What the chain learns, and what it does not

For every bundle the chain sees nullifiers, commitments, ciphertexts, the anchor and the value balance. For a shield, an unshield and a transfer's fee it sees an amount, and it ties shields and unshields to the signing account. An observer can count notes and spends, and can fingerprint a wallet that always pays the same fee. It cannot tell which note a nullifier spends, who owns a note or what it holds. The privacy is therefore bounded by the transparent edges, not by the circuit. A transfer's tip goes to the proposer's earnings account in the clear, and counts against the same 24 hour cap, so a proposer holding counterfeit notes cannot drain the pool through fees.

The wallet derives its keys with a hardened-only ZIP-32 scheme under an Orama personalisation, so no Orama key equals a Zcash Orchard key from the same seed. A build without cgo or without the Rust library links stubs that refuse every bundle. Such a node still runs the chain, but it cannot admit a shielded transaction, so every validator must run the full build. The first scaling limit is verification: every validator verifies every bundle twice, and the out-of-process verifier serialises its requests.

## Governance and contracts

`x/houses` is the only on-chain governance. The SDK's `x/gov` is not wired, and every stock module's authority is derived from a name no module will register, so a vote can do only seven things the module codes: change six houses parameters, schedule an upgrade, change the emission split, make a development spend, set power bounds, change relay reporters, and edit two allow-lists.

Governance opens by tiers, gated on live facts and not on a date. The parameter tier needs at least 21 eligible operators and either 271,000 ORAMA bonded or the power multiplier at 1. The structural tier needs the multiplier at 1, 21 operators, at least 7 distinct /16 prefixes and 5 ASNs. An eligible operator has an active node past the identity lock, 90 service days and a house bond of 1,000 ORAMA, with at most 3 per /16 and 5 per ASN.

There are two houses. The token house weights bonded stake: a delegator votes directly or inherits its validator's vote, and inherited stake per validator is capped at 3% of bonded stake. The operator house is one vote per eligible operator. A parameter change passes the token house with 40% quorum, then faces a 7 day veto window in which 30% of the house voting no kills it. A structural change needs the token house and more than half of the eligible operators. Timelocks are floors in code: 14 days for parameters, 7 for spends, 60 for upgrades and everything else, with no expedited path. An operator who votes both ways burns its entire house bond.

The six tunable houses parameters are the only numbers a vote can change. `x/nodes`, `x/shielded` and `x/storage` have no setter, so no vote can touch them. The emission schedule, the burn rule and the absence of freeze, blacklist, halt and pause are ossified: there is no field and no message. Proposing costs only the transaction fee and one of 64 active slots, and the proposer needs no stake.

Diversity is declared, not proved. An operator's /16 and ASN come from `x/nodes`, where the operator states them, so a house counts what operators claim. An identity lock, which makes a new identity wait before it counts, stops an operator from moving its identity to fit a single vote.

A proposal that hits a read fault is rolled back and retried, and fails after five attempts, so one bad proposal cannot halt the chain. An upgrade is a scheduled halt at an absolute height, which cosmovisor must still stage.

Contracts run on CosmWasm through wasmd, with `x/wasmpolicy` around it:

- **Norama stays norama.** A contract may hold and pay norama like any account, but may not wrap it as a token. It can pay a user's earnings through an earnings binding.
- **Closed uploads.** Until a sunset height (3,162,240 blocks), only genesis code and governed hashes may be stored. Five audited standard contracts, including `cw20-base` and `cw721-base`, ship at genesis.
- **Priced state.** A metered store counts bytes added and removed, and the caller locks a refundable deposit of 68,359 norama per byte, capped at 10 ORAMA per transaction. A shrink refunds 99%.
- **Priced code.** Code is permanent, so every message that stores it burns 68,359 norama per byte of the uncompressed wasm from the signer's bank balance, on top of gas, and never refunds it. The genesis contracts are not charged.
- **No IBC, no stargate, no staking.** A contract is always the signer of what it sends, since no binding has a sender field, and reaches Orama's modules only through bindings for tokens, NFTs, the market, storage and earnings. The shielded binding returns `NOT_LINKED`.

## Compressed NFTs and the market

`x/cnft` stores no assets. An asset is a 32-byte leaf hash in a concurrent Merkle tree, and the chain keeps only the tree: its root, a ring of recent changelogs, the rightmost path and an optional canopy. State size depends on the tree's shape (depth up to 30, buffer up to 2,048, canopy up to 14) and not on its leaf count. The creator pays a deposit of one norama per byte of modelled state, 31,588 norama for depth 14 with a buffer of 64.

The holder knows what they own and proves it. Every transfer, burn or metadata update carries the current leaf and a Merkle proof, and the owner or delegate signs. The chain hashes the supplied leaf, so it cannot be misstated. The keeper recomputes the root, then replays every changelog written since the proof's anchor, fixing the one sibling that each concurrent update can have changed (`chain/x/cnft/types/merkle.go:SetLeaf`). A proof therefore survives up to `buffer_size` updates to other leaves, and fails with a stale-proof error only if its own leaf moved. Minting needs no proof from the creator: the chain appends at its stored rightmost path, up to 64 leaves per message. Nonces make every state a different leaf, so an old proof cannot be replayed. An owner can decompress an asset into a plain state record and compress it back.

A creator decides what is minted and to whom, but cannot change a minted leaf's owner, the royalty, or delete the tree. Asset ids are chosen by the creator and are unique only among decompressed assets, so a buyer relying on an id should also check the tree and the `creator_hash` written at mint. A delegate can transfer, burn and update metadata but cannot list or decompress. Tree deposits are never released.

Only two messages emit an event, because they consume a record the message does not carry: a settle reports the listing, tree and buyer it consumed, and a compress the decompressed record it consumed. Everything else is rebuilt from transaction bodies. The indexer in [Storage deals, archive and indexer](ch18-storage-deals-archive-and-indexer.md) does this, and a wallet needs no trust in it because the chain verifies every proof.

`x/market` lists a leaf at a fixed price, accepts escrowed bids, and settles by buy-now or by the seller accepting a bid. A listing is not an escrow; settlement re-proves that the seller still owns the leaf. In one transaction it splits the price into a creator royalty (basis points copied from the collection at listing) and the seller's remainder, refunds every other bid, moves the leaf with the delegate cleared, and credits both shares to earnings accounts. Bids and buy-now payments are visible bank movements, but proceeds never become public balances.

![A sale: list, bid, buy now, split and pay](../technical-reference/diagrams/ch45-sale.svg)

Neither module has an authority, a pause or a parameter. The royalty binds only market sales, since a plain transfer pays none. Bids per listing are unbounded, so a listing flooded with dust bids can become too expensive to cancel or settle within a block's gas limit.
