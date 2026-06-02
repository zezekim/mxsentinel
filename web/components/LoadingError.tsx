interface Props {
  loading: boolean;
  error: string | null;
}

export default function LoadingError({ loading, error }: Props) {
  if (loading) return <p className="state-msg">Loading…</p>;
  if (error) return <p className="state-msg error">Error: {error}</p>;
  return null;
}
