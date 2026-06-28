import { getPageImage, source } from '@/lib/source';
import { notFound } from 'next/navigation';
import { generateOGImage } from 'fumadocs-ui/og';
import { appName } from '@/lib/shared';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export const revalidate = false;

const labelColor = 'rgb(255,255,255)';
const accentGradient = 'rgba(136,95,239,0.35)';

const iconDataUri = `data:image/png;base64,${readFileSync(
  join(process.cwd(), 'public/icons/512x512.png'),
).toString('base64')}`;

export async function GET(_req: Request, { params }: RouteContext<'/og/docs/[...slug]'>) {
  const { slug } = await params;
  const page = source.getPage(slug.slice(0, -1));
  if (!page) notFound();

  return generateOGImage({
    title: page.data.title,
    description: page.data.description,
    site: appName,
    primaryColor: accentGradient,
    primaryTextColor: labelColor,
    icon: <img src={iconDataUri} width={64} height={64} alt="" />,
    width: 1200,
    height: 630,
  });
}

export function generateStaticParams() {
  return source.getPages().map((page) => ({
    lang: page.locale,
    slug: getPageImage(page).segments,
  }));
}
