# Echo 115 CAS Behavior Note

This note records the operational distinction between three different 115
artifacts that were easy to conflate during Task 14 follow-up work:

- a live 115 copy already present in Echo's `file_copies`
- a plain 115 `.cas` payload that carries only `sha1` and `preID`
- a share-backed 115 `.cas` payload whose CAS metadata also includes
  `source.type=115_share`

All private share identifiers, receive codes, file IDs, and archive paths are
intentionally omitted from this repository note.

## What Echo Uses At Serve Time

Echo does not serve media by reconstructing from `.cas` on every request.
`/api/restore/{file_id}` and `/api/stream/{file_id}` resolve a library entry to
live copies and ask the sidecar for a direct link or byte stream from those
copies.

Operational implication:

- If Echo has at least one healthy live copy, playback can proceed.
- If Echo has no live copy, the current serve path returns exhaustion (404 or
  503 depending on failure reason); it does not automatically re-import from a
  stored `.cas` tree.

## Plain 115 `.cas`

The current 115 CAS payload stores `sha1` and `preID`. That is enough for the
fast path where the 115 rapid-upload API accepts those fields as-is.

It is not enough for every case. When the 115 upload-init API returns a
`sign_check` range challenge, the restore path must read the requested source
byte range and compute an additional SHA1 digest for that range.

Operational implication:

- A plain 115 `.cas` is not a fully offline, always-restorable asset.
- It can restore successfully when 115 does not demand the extra range digest.
- It can fail when 115 demands `sign_check` and no source bytes are available.

## Share-Backed 115 `.cas`

Some 115 `.cas` trees carry CAS `source` metadata with `type=115_share`. In
that form, the sidecar can satisfy a `sign_check` challenge by receiving the
shared file into a logged-in 115 account, obtaining an account download URL for
the temporary file, and reading the requested byte range from that received
copy.

Operational implication:

- A share-backed 115 `.cas` is stronger than a plain `sha1 + preID` CAS.
- It still depends on the referenced share remaining valid and receivable.
- If the backing share dies, the CAS loses the ability to fetch source bytes
  for `sign_check` and can fall back to the same failure mode as plain 115 CAS.

## Reliability Order

For Echo operations, the useful reliability order is:

1. live copy already present in a mounted 115 account
2. share-backed 115 `.cas` with a still-valid share source
3. plain 115 `.cas` containing only `sha1 + preID`
4. an unprocessed third-party share link

This order answers two different questions at once:

- "Can Echo serve the file right now?"
- "Can the operator still re-ingest or re-restore the file later?"

Only the first item is directly serveable by Echo today. The middle items are
re-ingest assets, not active playback assets.

## Operator Guidance

- Prefer live copies for anything that must remain continuously playable.
- Keep `.cas tree + manifest.jsonl` as re-ingest material, not as proof that a
  file is immediately playable.
- Do not assume `115share2cas` output is fully offline-restorable unless the
  exact payload format and source durability have been checked.
- When auditing a private archive, inspect a sample `.cas` payload and confirm
  whether it includes `source.type=115_share` before making durability claims.
