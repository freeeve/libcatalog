# 129 -- Work cards: link the tag/subject chips AND contributor names to their term pages

Filed from libcatalog-demo (owner requests: "can we make these subjects in the
cards clickable in the browse" + "clickable authors on browse cards too").

On the works list, `work-card.html` renders both as plain text while the SAME
values on the Work detail page are taxonomy links:

- chips, line ~19: `<ul class="lcat-tags">{{ range first 5 . }}<li>{{ . }}</li>
  {{ end }}</ul>`
- contributors, line ~15: `{{ range $i, $c := first 4 . }}{{ if $i }}; {{ end }}
  {{ $c.name }}{{ with $c.role }} <span class="lcat-role">({{ . }})</span>
  {{ end }}{{ end }}`

Browsing users see chips that render identically to the detail page's linked
chips (and author names right next to a linked title) that do nothing.

Ask: on cards, link each chip to its tag/subject term page and each contributor
name to its contributor term page, exactly as the detail page does; keep
`data-pagefind-filter` consistent between the two surfaces. Depends on /
interacts with tasks/128: build hrefs from the term's actual .RelPermalink (look
the term up in site.Taxonomies) so dotted names ("Kuang, R.F.") don't 404.
Keep the role suffix ("(author)") outside the link text.

Per the demo's policy it carries no local shadow for this; the demo picks it up
on the next module bump once it ships.
