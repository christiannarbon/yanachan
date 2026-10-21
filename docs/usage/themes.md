# Themes

The picker in the top corner switches the whole palette, corner radii and
typefaces. Ten themes ship, in three groups:

- **Yanami Anna — Calorie Meter**, the default, and the reason this repo exists.
- **Studio Paper**, the house palette.
- Eight derived from paintings: Cézanne, Hokusai, Hopper, Matisse, Monet, two
  Van Goghs, Wang Ximeng.

The choice is remembered per browser under `yana.art`. Webfonts for a theme are
fetched only when it is first selected, so nine of the ten cost you nothing
until you try them.

All themes are light. There is no dark mode, by design.

## The change itself

Picking a theme does not snap. The new palette wipes in from the left edge
behind a soft gradient, about two thirds of a second wide, so the change reads
as one movement across the page rather than ten colours swapping at once.

It is decoration and nothing depends on it. A browser without view transitions
changes the palette outright, and so does the app if your system asks for
reduced motion.

## Yanami's palette

Not invented. Every colour is one the official 負けヒロインが多すぎる！ site
declares in its own `:root`, and both faces are the pair the
[calorie meter page](https://makeine-anime.com/special/calorie_meter/) sets its
numbers in:

| Token | Where it comes from                                          |
| --- |--------------------------------------------------------------|
| `#070a7d` navy | the site's structure — rules, buttons, the 2px card outline  |
| `#ff7031` vermillion | Just there because it vibrant and what the page shouts with |
| `#fff100` yellow | Yanami's main character color                                |
| `#dae9f5` pale blue | Yanami's main character color                                |
| Quantico | the big numerals                                             |
| Noto Sans JP | everything else                                              |

The role assignment is the part worth explaining. Navy takes `primary`, so it
becomes `--fact` and the accent — that is what the site is built out of. The
vermillion takes `secondary`, which is what lands on `--dim`, the colour this
app paints down the left edge of a pull request that needs you. Her colour, on
the thing that is asking for attention.

## Contrast

Every palette goes through a generator that checks the worst-case contrast ratio
for every colour on every surface it is actually painted on. The results are
committed in `frontend/src/styles/art-themes.audit.md`, so a theme's
accessibility is a file you can read rather than a claim.

## Adding one

Themes are data, not code: adding a palette is an entry in the generator, not a
refactor of any component. See
[theming system](../tech/low-level-design/theming-system.md).
