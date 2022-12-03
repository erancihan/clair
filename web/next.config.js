const path = require('path');

module.exports = {
  reactStrictMode: true,
  swcMinify: true,
  basePath: '/site',
  images: {
    unoptimized: true,
  },
  sassOptions: {
    includePaths: [path.join(__dirname, 'styles')],
  },
}
