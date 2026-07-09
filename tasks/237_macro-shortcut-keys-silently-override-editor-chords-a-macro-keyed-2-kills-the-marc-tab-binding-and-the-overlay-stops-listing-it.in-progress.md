# 237 -- macro shortcut keys silently override editor chords: a macro keyed 2 kills the MARC tab binding, and the ? overlay stops listing it

Filed from libcat on 2026-07-09 (cross-repo ask).

## Symptom

Give a macro the shortcut key `2`. Open any work editor and press `2`. Instead of
switching to the MARC tab, the macro stages its edits. The MARC-tab binding is
gone -- not shadowed, *gone*: the `?` overlay no longer lists it either.

Measured on the 8481 playground through the real UI (`ui/probe_keybindings.mjs`),
with a copycat-minted sentinel work and a sentinel macro:

```
PASS K1  CONTROL: "2" opens the MARC tab       with no macro bound, "2" shows the MARC grid
FAIL K2  a colliding macro shortcut is refused POST /v1/macros keys="2" -> 201
FAIL K3  "2" still opens the MARC tab          MARC grid present=false; the macro staged its tag instead=true
PASS K4  one key does exactly one thing        only the macro fired
FAIL K5  the "?" overlay reveals the collision overlay mentions MARC tab=false, apply macro=true
```

`K1` is the control: the same keypress on the same record, before the macro
exists, opens the MARC tab. `K4` passing is the sting -- exactly one action
fires, and it is the wrong one.

Nothing validates the key, anywhere:

```
POST /v1/macros keys="2"   -> 201   (collides with the editor's MARC-tab chord)
POST /v1/macros keys="7"   -> 201
POST /v1/macros keys="7"   -> 201   (a second macro on the same key)
POST /v1/macros keys="zz"  -> 201   (two characters; MacroBar ignores it, so it is silently inert)
POST /v1/macros keys="?"   -> 201   (reserved: "?" opens the help overlay)
```

And the Shortcut key field's placeholder is `1` (`Macros.svelte:200`) -- which is
the editor's Native-tab chord. The UI actively suggests a colliding key.

## Root cause

`backend/ui/src/lib/keyboard.ts:148-158` registers bindings by overwriting:

```js
export function bindKeys(scope: string, map: Record<string, BindingSpec>): () => void {
  const m = scopeMap(scope);
  for (const [defaultKey, spec] of Object.entries(map)) {
    const key = keymap[id] ?? defaultKey;
    m.set(key, b);          // <- last writer wins, silently
  }
```

The registry is a `Map<key, Binding>` with one slot per key. `setKeymapEntry`
(`keyboard.ts:260`) is careful -- it refuses a remap onto a `RESERVED` chord or a
`conflictingBinding` -- but that guard sits on the *user remap* path only.
Nothing checks a collision when a binding is registered in the first place.

Two registrants share the `"editor"` scope:

- `WorkEditor.svelte:108` claims `1`, `2`, `3`, `p`, `m`, `mod+s`
- `MacroBar.svelte:65` calls `bindKeys("editor", map)` with one entry per macro
  whose `keys` is a single character

`MacroBar` wins because it binds *later*: its `onMount` calls `void load()`,
which awaits `fetchMacros()` before `bindShortcuts()`, so the registration lands
after `WorkEditor`'s synchronous `onMount`. The editor's `Binding` object is
evicted from the map, which is why the `?` overlay -- which renders from the same
registry -- stops showing the MARC-tab row.

The eviction also breaks teardown. `bindKeys`'s unbind deletes by identity:

```js
if (m.get(b.key) === b) m.delete(b.key);
```

`WorkEditor`'s binding is no longer the value at `"2"`, so its unbind is a no-op
and the macro's binding outlives the editor's own cleanup.

Server-side, `batch/macros.go:31` carries `Keys string` with no validation, and
the create handler stores whatever arrives.

## Why it matters

A cataloger sets up a macro shortcut and, without any feedback, disables one of
the six chords the editor advertises in its own footer legend. They will not
connect the two: the macro screen and the editor are different screens, and the
`?` overlay -- the one place to check what a key does -- has already forgotten
the binding it replaced.

The collision is not exotic. The editor's single-character keys are `1 2 3 p m`,
and macro shortcuts are single characters by construction (`maxlength="1"`). The
placeholder proposes `1`. Anyone assigning shortcuts to their first three macros
in the obvious way takes out all three tab chords.

The severity is bounded -- nothing is written, and pressing `2` stages edits the
cataloger can discard -- but "a keystroke I have used for months now does
something else, and nothing on screen admits it" is a bad property for a tool
people live in all day.

## Expected

- `bindKeys` should not silently evict. At minimum, warn on collision; better,
  refuse the later registration and surface it, since `conflictingBinding` already
  computes exactly this.
- Macro shortcuts should be validated where they are chosen. `Macros.svelte`
  knows the editor scope's keys via the registry: reject a key that is `RESERVED`
  or already bound in `"editor"`, the same check `setKeymapEntry` performs, and
  say which action holds it.
- Reject duplicates and multi-character `keys` server-side
  (`batch/macros.go`), so a macro cannot be stored with a shortcut that can never
  fire (`"zz"`) or one that shadows another macro.
- The placeholder should not be `1`. Suggest a key the editor does not claim.
- If a collision is ever allowed deliberately, the `?` overlay must show both
  rows rather than losing one.

## Repro

```
cd ~/libcat-e2e && node ui/probe_keybindings.mjs
```

Expect `K2`, `K3` and `K5` to flip to PASS, with `K1` (the control) and `K4`
staying green. The probe mints its own sentinel work and macro, only ever
*stages* the macro's op, and tombstones the work and deletes the macro on the way
out. `harness/retest.mjs` carries the same check as `t237`.
