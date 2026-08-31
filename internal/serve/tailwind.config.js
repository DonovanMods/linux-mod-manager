/**
 * Config for the Makefile's `css` target (dev-time only tool - see that
 * target's comment in the repo root Makefile). `content` tells Tailwind
 * where to scan for the utility class names actually in use, so the built
 * app.css only carries what these pages reference.
 *
 * @type {import('tailwindcss').Config}
 */
module.exports = {
  content: ["./templates/**/*.gohtml", "./static/*.js"],
  theme: {
    extend: {},
  },
  plugins: [],
};
