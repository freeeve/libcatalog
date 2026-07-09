# 261 -- attachments are written grain-first: a failed byte Put leaves a phantom entry that lists but 404s on download, and DELETE discards its bs.Delete error so a failed removal reports 204 while the bytes stay on disk

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).
