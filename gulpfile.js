const gulp = require('gulp');

const postcss = require('gulp-postcss');
const tailwindcss = require('@tailwindcss/postcss');
const autoprefixer = require('autoprefixer');

// generate static css file from templ files using tailwindcss
gulp.task('css', function () {
    return gulp
        .src('./web/css/main.css')
        .pipe(
            postcss([
                tailwindcss('./tailwind.config.js'),
                autoprefixer(),
                require('cssnano')()
            ]))
        .pipe(require('gulp-rename')({ extname: '.css' }))
        .pipe(gulp.dest('web/static/css'));
});

gulp.task('watch', function () {
    gulp.watch('web/**/*.templ', gulp.parallel('css', 'go-server'));
});

gulp.task('default', gulp.series('css', 'watch'));
