import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();

const config = {
  reactStrictMode: true,
  output: 'export',
  async redirects() {
    return [
      { source: '/guides', destination: '/guides/native-install', permanent: true },
      { source: '/reference', destination: '/reference/cli-reference', permanent: true },
      { source: '/configuration', destination: '/configuration/configuration-model', permanent: true },
    ];
  },
};

export default withMDX(config);
