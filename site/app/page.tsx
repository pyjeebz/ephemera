import { Compare } from "@/components/site/compare"
import { Effects } from "@/components/site/effects"
import { Faq } from "@/components/site/faq"
import { Features } from "@/components/site/features"
import { Footer } from "@/components/site/footer"
import { Header } from "@/components/site/header"
import { Hero } from "@/components/site/hero"
import { Quickstart } from "@/components/site/quickstart"
import { UseCases } from "@/components/site/use-cases"

export default function Home() {
  return (
    <>
      <Header />
      <main>
        <Hero />
        <Features />
        <UseCases />
        <section className="section quickstart" id="get-started">
          <h2>Your first sandbox in a minute</h2>
          <Quickstart />
        </section>
        <Compare />
        <Faq />
      </main>
      <Footer />
      <Effects />
    </>
  )
}
