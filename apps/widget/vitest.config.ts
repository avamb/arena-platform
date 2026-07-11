import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [
    svelte({
      hot: false,
      // Do not set customElement: true globally — sub-components would need
      // explicit tag names.  ArenaTickets.svelte declares via <svelte:options>.
    }),
  ],
  test: {
    environment: 'node',
    globals: false,
    include: ['src/**/*.test.ts'],
  },
});
