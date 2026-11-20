// Side-effect stylesheet imports (main.tsx imports app.css). Bun's bundler
// pulls them into the page's <style>; the type checker only needs to know
// they are modules.
declare module "*.css";
