// Home serves "/" and "/index.html" from index.html and "/management.html" from
// management.html; both are the same single-page app.
import { copyFileSync } from 'node:fs';
copyFileSync('dist/index.html', 'dist/management.html');
console.log('postbuild: management.html written');
