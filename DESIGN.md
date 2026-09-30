# Design world — P2P Search

## Direction
**Signal desk.** A calm operator console for peering into eD2K/KAD: deep slate surfaces, one teal signal accent, precise type, dense results. Not a landing page, not a purple SaaS kit.

## Tokens
| Token | Value | Role |
| --- | --- | --- |
| `--bg` | `#14181f` | Page ground |
| `--surface` | `#1c222c` | Panels / inputs |
| `--surface-2` | `#242b38` | Hover / raised |
| `--text` | `#e9edf4` | Primary text |
| `--muted` | `#8f98a8` | Secondary |
| `--accent` | `#2fad9a` | Primary action / live signal |
| `--accent-dim` | `rgba(47,173,154,0.16)` | Active nav / soft fills |
| `--border` | `#2e3645` | Hairlines |
| `--ok` | `#3ecf8e` | Ready |
| `--warn` | `#d4a054` | Pending |
| `--err` | `#e07070` | Errors |

## Type
- UI: **IBM Plex Sans** (400–700)
- Data / monospace fields: **IBM Plex Mono**
- Scale: 0.8125 → 0.9375 → 1.0625 → 1.375 → 2.25rem (Operate, not fluid display)

## Layout
- Sticky top nav with brand mark + two links
- Search page: brand-led hero, search shell, status strip, results table (not cards)
- Settings: stacked field blocks, no nested cards
- Max content width ~1120px

## Motion
State only: button press `scale(0.97)`, toast enter, focus ring. No page-load choreography. Respect `prefers-reduced-motion`.

## Anti-references
No purple gradients, no cream+terracotta, no equal feature cards, no ALL-CAPS eyebrows, no glass-for-decoration.
