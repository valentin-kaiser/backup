// `ng build` empties the output directory. The Go package embeds it, so it has to keep at least one
// tracked file for `go build` to work without a built UI.
import { mkdirSync, writeFileSync } from 'node:fs';

mkdirSync('../pkg/web/static', { recursive: true });
writeFileSync('../pkg/web/static/PLACEHOLDER', '');
