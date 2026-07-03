import { getPageImage, source } from '@/lib/source';
import { notFound } from 'next/navigation';
import { ImageResponse } from 'next/og';
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

  return new ImageResponse(
    (
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          width: '100%',
          height: '100%',
          color: 'white',
          padding: '4rem',
          backgroundColor: '#0c0c0c',
          borderBottom: `18px solid ${accentGradient}`,
        }}
      >
        <p style={{ fontWeight: 800, fontSize: '82px', margin: 0 }}>{page.data.title}</p>
        <p
          style={{
            fontSize: '52px',
            color: 'rgba(240,240,240,0.8)',
            margin: 0,
            marginTop: '16px',
            paddingBottom: '28px',
          }}
        >
          {page.data.description}
        </p>
        <div
          style={{
            display: 'flex',
            flexDirection: 'row',
            alignItems: 'center',
            gap: '20px',
            marginTop: 'auto',
            color: labelColor,
          }}
        >
          <img src={iconDataUri} width={64} height={64} alt="" />
          <p style={{ fontSize: '56px', fontWeight: 600, margin: 0 }}>{appName}</p>
        </div>
      </div>
    ),
    { width: 1200, height: 630 },
  );
}

export function generateStaticParams() {
  return source.getPages().map((page) => ({
    lang: page.locale,
    slug: getPageImage(page).segments,
  }));
}
