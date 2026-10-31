# Brand

**Mini**, the minime mark: a person, not a robot or a creature. A round head, shoulders and
a face, and one curl on top that's their own mind, a thought growing like a sprout. In living
green, the same family as the mind console.

| File | Use |
|---|---|
| [mark.svg](mark.svg) | The mark, at any size. Also the favicon of the console and the profile page. |
| [mark-512.png](mark-512.png), [mark-192.png](mark-192.png), [mark-64.png](mark-64.png), [mark-32.png](mark-32.png) | The mark as PNG. |
| [banner-dark.png](banner-dark.png), [banner-light.png](banner-light.png) | The README banner, 1280 by 358. |
| [social.png](social.png) | GitHub's social preview, 1280 by 640 (Settings, then General, then Social preview). |
| [x-avatar.png](x-avatar.png) | The X (Twitter) profile picture, 800 by 800; X shows it as a circle. |
| [x-header.png](x-header.png) | The X header, 1500 by 500, with the words clear of the profile picture. |
| [banner.html](banner.html) | The source of all of these: open it, or render it with `?light`, `?social`, `?header` or `?avatar`. |
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

The others are the same page: `?social` at 1280 by 640, `?header` at 1500 by 500, and
`?avatar` at 400 by 400 with `--force-device-scale-factor=2`. For example:

```bash
cd docs/brand && "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --hide-scrollbars --virtual-time-budget=8000 --window-size=1280,640 --screenshot="$PWD/social.png" "file://$PWD/banner.html?social"
```
