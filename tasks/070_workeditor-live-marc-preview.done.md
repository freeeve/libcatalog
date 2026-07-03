# 070 -- Work editor: live MARC preview and split-pane density

## Context

The MARC tab (tasks/049) decodes the saved grain only: staged native ops
are invisible in MARC until saved, and the tab layout means one view at a
time. tasks/064 already compacted the header and moved fields to two
columns; the remaining density win is horizontal -- use wide screens for a
second pane instead of squeezing more rows.

## Scope

1. **Preview endpoint**: `POST /v1/works/{id}/marc/preview` applies staged
   ops to the current doc, encodes MARC, and returns field arrays +
   knownLoss -- the dry-run pipeline plus the tasks/049 encoder, no writes.
2. **Split pane**: on wide viewports the native form and a read-only live
   MARC pane render side by side; preview refreshes debounced on
   stage/unstage; fields differing from the saved grain are highlighted.
   Verbatim/lossy fields render as the grid does. Narrow viewports keep
   the current tabs; the pane toggle gets a keyboard shortcut.
3. **Density remainder**: collapsible instance sections with a summary
   line (instance id, profile, item count) so multi-instance works scan
   without scrolling.

## Acceptance

- Staging a native title edit updates 245 in the MARC pane without saving;
  discarding ops snaps the pane back to the saved state.
- The pane is read-only and marks known-loss fields; the MARC tab's
  editing path is unchanged.
- Narrow viewports behave exactly as today.

## Outcome note

A native work-title edit surfaces as MARC **240** (the Work's title in the
BIBFRAME->MARC crosswalk); 245 stays the Instance's transcribed title, which
the instance profile does not expose. The preview shows the change live
either way -- the 245 wording above assumed the crosswalk mapped work title
to 245, and it correctly does not.
