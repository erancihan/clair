const gulp = require('gulp');

const postcss = require('gulp-postcss');
const tailwindcss = require('@tailwindcss/postcss');
const autoprefixer = require('autoprefixer');

// Build the stylesheet from the templ sources. Tailwind v4 finds the class
// names by scanning the project itself; there is no config file to point it
// at, and none is needed.
gulp.task('css', function () {
    return gulp
        .src('./internal/web/css/main.css')
        .pipe(postcss([tailwindcss(), autoprefixer(), require('cssnano')()]))
        .pipe(require('gulp-rename')({ extname: '.css' }))
        .pipe(gulp.dest('./internal/web/static/css'));
});

// Rebuild the CSS when a template changes. The Go binary is rebuilt by air
// (`make dev`), which is the other half of the loop.
gulp.task('watch', function () {
    gulp.watch('./internal/web/**/*.templ', gulp.series('css'));
});

gulp.task('default', gulp.series('css', 'watch'));
