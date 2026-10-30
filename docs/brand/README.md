# Brand

**Mini**, the minime mark: a person, not a robot or a creature. A round head, shoulders and
a face, and one curl on top that's their own mind, a thought growing like a sprout. In living
green, the same family as the mind console.

| File | Use |
|---|---|
| [mark.svg](mark.svg) | The mark, at any size. Also the favicon of the console and the profile page. |
| [mark-512.png](mark-512.png), [mark-192.png](mark-192.png), [mark-64.png](mark-64.png), [mark-32.png](mark-32.png) | The mark as PNG. |
| [banner-dark.png](banner-dark.png), [banner-light.png](banner-light.png) | The README banner, 1280 by 358. |
| [banner.html](banner.html) | The banner's source: open it, or render it with `?light`. |
| [concepts.html](concepts.html) | The four concepts it was chosen from. |

| Colour | Hex |
|---|---|
| Head, accent | `#2fcb6b` |
| Shoulders | `#0e7c4a` |
| Cheeks | `#b6f5c9` |
| Eyes, ink | `#0b1f14` |

The wordmark is "minime" in Fredoka SemiBold, lowercase, with "me" in the accent green.

To render the banners again (Chrome headless):

```bash
cd docs/brand && "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --hide-scrollbars --virtual-time-budget=8000 --window-size=1280,358 --screenshot="$PWD/banner-dark.png" "file://$PWD/banner.html"
```
