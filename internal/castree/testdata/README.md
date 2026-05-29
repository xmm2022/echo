CAS fixture notes
=================

`115-film.mkv.cas` was generated from upstream
`xmm2022/openlist-guangyapan-src@3324d78d9de9a060bc6830d0f4b2d012ea47576b`
(`feat/cas-tools`) by running a temporary `main` inside that checkout:

```go
body, err := casmeta.Encode(&casmeta.Info{
    Provider: "115",
    Name:     "Film.mkv",
    Size:     123456789,
    SHA1:     strings.Repeat("A", 40),
    PreID:    strings.Repeat("B", 40),
})
```

`139-x.txt.cas` was generated from the same checkout with:

```sh
python3 - <<'PY'
from tools.cas139.cas_common import encode_cas
print(encode_cas('x.txt', 12, 'a'*64).decode('ascii'))
PY
```

The hash values are deterministic test values, not live account data. Upstream
code writes `create_time` from the current clock; tests only require that field
to decode as a non-empty string.
