import Link from 'next/link';
import Image from 'next/image';
import { buttonVariants } from 'fumadocs-ui/components/ui/button';

export default function HomePage() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-6 py-24 text-center">
      <div className="mx-auto flex max-w-3xl flex-col items-center gap-6">
        <Image src="/images/logo.svg" alt="" width={64} height={64} unoptimized className="size-16" />
        <div className="space-y-3">
          <p className="text-sm font-medium text-fd-muted-foreground">
            Public hostnames and private Service routes through Cloudflare Tunnel
          </p>
          <h1 className="text-4xl font-bold tracking-tight sm:text-6xl">KFlared Docs</h1>
          <p className="mx-auto max-w-2xl text-balance text-lg text-fd-muted-foreground">
            Deploy and configure KFlared, understand its trust boundaries, then operate, test, and release the
            controller.
          </p>
        </div>
        <div className="flex flex-wrap justify-center gap-3">
          <Link
            href="/installation"
            className={buttonVariants({ variant: 'primary', className: 'h-10 px-4' })}
          >
            Get started
          </Link>
          <Link
            href="/architecture"
            className={buttonVariants({ variant: 'outline', className: 'h-10 px-4' })}
          >
            Understand the system
          </Link>
        </div>
        <p className="text-sm text-fd-muted-foreground">
          Already running KFlared?{' '}
          <Link href="/operations" className="underline">
            Observe and troubleshoot it
          </Link>
          . Contributing? Start with{' '}
          <Link href="/testing" className="underline">
            testing
          </Link>{' '}
          and the{' '}
          <Link href="/release-process" className="underline">
            release process
          </Link>
          .
        </p>
      </div>
    </div>
  );
}
