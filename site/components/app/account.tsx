"use client"

import { createContext, useContext, type ReactNode } from "react"

import { type Account } from "@/lib/api"
import { useApi } from "@/lib/use-api"

const AccountContext = createContext<Account | undefined>(undefined)

// SignedIn reads who is signed in for the pages under it, and sends
// anyone who isn't to sign in.
export function SignedIn({ children }: { children: ReactNode }) {
  const { data } = useApi<Account>("/v1/me")
  return <AccountContext value={data}>{children}</AccountContext>
}

export function useAccount() {
  return useContext(AccountContext)
}
