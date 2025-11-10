/** @type {import('tailwindcss').Config} */
export default {
  content: ["./src/**/*.{astro,html,js,jsx,ts,tsx}"],
  theme: {
    extend: {
      colors: {
        "ep-ink": "#0f172a",
        "ep-surface": "rgba(15,23,42,0.8)",
        "ep-glow": "rgba(56,189,248,0.18)",
      },
      boxShadow: {
        "ep-card": "0 24px 50px -12px rgba(15, 23, 42, 0.7)",
      },
      fontFamily: {
        sans: ["Inter", "system-ui", "sans-serif"],
      },
    },
  },
  plugins: [],
};
